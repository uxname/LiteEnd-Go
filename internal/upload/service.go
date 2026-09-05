// Package upload accepts multipart file uploads and stores them in an
// S3-compatible object store shared by every replica.
package upload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/logger"
)

// ErrDisallowedMime is returned by ProcessFile when the uploaded content-type is
// not in the image allowlist. Callers skip such files rather than failing.
var ErrDisallowedMime = errors.New("disallowed mime type")

// ErrFileTooLarge is returned by ProcessFile when the uploaded file exceeds
// UploadMaxFileSize. Nothing is stored before returning.
var ErrFileTooLarge = errors.New("file too large")

// sniffLen is the number of leading bytes inspected for content-based MIME
// detection (http.DetectContentType only looks at the first 512 bytes).
const sniffLen = 512

// maxExtLen caps the extension carried from the client-supplied filename into
// the object key (".jpeg" is 5). Anything longer is dropped, not truncated.
const maxExtLen = 10

var allowedMimeTypes = map[string]struct{}{ //nolint:gochecknoglobals // static mime allowlist
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

// Writer is the subset of sqlc used to persist upload metadata.
type Writer interface {
	CreateUpload(ctx context.Context, arg sqlc.CreateUploadParams) (sqlc.Upload, error)
}

// objectStore is the subset of the S3 client used to store uploaded objects.
// Every replica writes to the same bucket, so a file stored by one is readable
// through all the others.
type objectStore interface {
	PutObject(
		ctx context.Context, bucket, key string, r io.Reader, size int64, opts minio.PutObjectOptions,
	) (minio.UploadInfo, error)
	RemoveObject(ctx context.Context, bucket, key string, opts minio.RemoveObjectOptions) error
}

// Service stores uploaded files as objects and records their metadata.
type Service struct {
	q         Writer
	store     objectStore
	bucket    string
	publicURL string
}

// New builds an upload Service backed by the configured object store. A
// malformed storage endpoint stops the boot here rather than surfacing on the
// first upload — the composition root is the only place that can still refuse
// to start.
func New(cfg *config.Config, q Writer) (*Service, error) {
	endpoint, secure := parseEndpoint(cfg.S3Endpoint, cfg.S3UseSSL)
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("build object storage client: %w", err)
	}
	return &Service{
		q:         q,
		store:     client,
		bucket:    cfg.S3Bucket,
		publicURL: cfg.S3PublicBaseURL,
	}, nil
}

// parseEndpoint splits the configured storage address into what minio-go wants:
// a host[:port] plus a TLS flag. S3_ENDPOINT is documented with a scheme
// (http://garage:3900), which minio.New rejects, and an https address means TLS
// whatever S3_USE_SSL says.
func parseEndpoint(raw string, useSSL bool) (host string, secure bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, useSSL
	}
	return u.Host, useSSL || u.Scheme == "https"
}

// SavedFile is the per-file result returned to the client. Path is the absolute
// public URL of the stored object: the browser loads it straight from the
// storage, not through this app.
type SavedFile struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
	// internal metadata (not serialised in the public response)
	key              string
	data             []byte
	originalFilename string
	extension        string
	size             int64
	mimetype         string
}

// AllowedMime reports whether a declared content-type is accepted.
func AllowedMime(mimetype string) bool {
	_, ok := allowedMimeTypes[mimetype]
	return ok
}

// ProcessFile validates a single uploaded file and returns its metadata,
// including the key and public URL it will get. The bytes are held until
// SaveMetadata stores them, so a request rejected while its parts are still
// being read leaves nothing in the bucket. It returns ErrDisallowedMime if the
// content-type is not allowed, and ErrFileTooLarge if the body exceeds
// UploadMaxFileSize.
//
// The context is unused: validating and buffering touch nothing cancellable —
// every remote call of an upload happens in SaveMetadata.
func (s *Service) ProcessFile(
	_ context.Context, originalFilename, mimetype string, body io.Reader,
) (*SavedFile, error) {
	// Cheap early reject on the client-declared content-type before reading on.
	if !AllowedMime(mimetype) {
		return nil, ErrDisallowedMime
	}

	// Content-based validation: sniff the leading bytes and trust the detected
	// type, not the client-supplied header (which is trivially spoofable).
	head, body, err := sniff(body)
	if err != nil {
		return nil, err
	}
	detected := detectMime(head)
	if !AllowedMime(detected) {
		return nil, ErrDisallowedMime
	}
	// The body is buffered whole: it is capped at UploadMaxFileSize (5 MiB) per
	// file and at BodyLimit (10 MiB) per request, and PutObject stores a known
	// size in one atomic PUT instead of a multipart upload. Raising either cap
	// raises this memory ceiling with it.
	data, err := io.ReadAll(io.LimitReader(body, config.UploadMaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(data)) > config.UploadMaxFileSize {
		return nil, ErrFileTooLarge
	}

	key := objectKey(time.Now().UTC(), originalFilename)
	return &SavedFile{
		Filename:         path.Base(key),
		Path:             s.publicURL + "/" + key,
		key:              key,
		data:             data,
		originalFilename: originalFilename,
		extension:        path.Ext(key),
		size:             int64(len(data)),
		mimetype:         detected,
	}, nil
}

// objectKey builds the key an upload is stored under: the date layout plus a
// random name. The extension is the only part that comes from the client, and
// object keys are paths — a separator or a ".." inside one would lift the object
// out of its date prefix, so the extension is sanitised instead of copied.
func objectKey(t time.Time, originalFilename string) string {
	return path.Join(relativeDir(t), uuid.NewString()+safeExt(originalFilename))
}

// safeExt returns the extension of name when it is a short, purely alphanumeric
// suffix, and "" otherwise. Everything a key must never carry — "/", "\", "..",
// spaces, control or unicode characters — fails that test.
func safeExt(name string) string {
	ext := path.Ext(name)
	if len(ext) < 2 || len(ext) > maxExtLen {
		return ""
	}
	for _, r := range ext[1:] {
		if !isASCIIAlnum(r) {
			return ""
		}
	}
	return ext
}

func isASCIIAlnum(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// relativeDir is the date prefix every object key starts with: YYYY/MM/DD/HH-MM.
func relativeDir(t time.Time) string { return t.Format("2006/01/02/15-04") }

// sniff reads up to sniffLen leading bytes for content detection and returns a
// reader that replays them ahead of the unread remainder, so the full stream is
// still stored.
func sniff(body io.Reader) (head []byte, full io.Reader, err error) {
	buf := make([]byte, sniffLen)
	n, readErr := io.ReadFull(body, buf)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return nil, nil, fmt.Errorf("read upload head: %w", readErr)
	}
	head = buf[:n]
	return head, io.MultiReader(bytes.NewReader(head), body), nil
}

// detectMime returns the sniffed MIME type without any charset parameters.
func detectMime(head []byte) string {
	detected := http.DetectContentType(head)
	if mt, _, err := mime.ParseMediaType(detected); err == nil {
		return mt
	}
	return detected
}

// RemoveFiles drops the buffered bytes of a batch the caller will not report to
// the client. It is not a rollback and cannot be one: SaveMetadata is what
// reaches the object store, and it cleans up after itself. Called before
// SaveMetadata, nothing is stored yet; called after one failed, the files it
// already committed stay committed — see SaveMetadata on per-file atomicity.
func (s *Service) RemoveFiles(files []*SavedFile) {
	for _, f := range files {
		f.data = nil
	}
}

// removeObject deletes the object of the one file whose row insert failed — the
// only object a rollback may ever touch, since every earlier file of the batch
// is already committed with its row. It detaches from ctx first: the failure
// being cleaned up may be that very cancellation.
func (s *Service) removeObject(ctx context.Context, f *SavedFile) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.FileUploadTimeout)
	defer cancel()

	if err := s.store.RemoveObject(ctx, s.bucket, f.key, minio.RemoveObjectOptions{}); err != nil {
		// Left behind, the object is unreferenced but still stored, so its key
		// has to reach the log — this line is the only trace of it.
		logger.From(ctx).Warn("upload rollback failed", "key", f.key, "error", err.Error())
	}
}

// SaveMetadata commits a validated batch one file at a time: a file's object is
// stored and its row inserted before the next file is started.
//
// Atomicity is per file, not per batch. The query layer exposes no transaction
// to span a batch, so this promises only what it can keep: the pair "object +
// row" is all-or-nothing, and a batch that fails half-way keeps the files it
// already committed and rolls back just the file it failed on — that object is
// removed, and the files after it are never stored at all.
//
// The invariant is that no failure path leaves a row pointing at a missing
// object, since that row is unusable and nothing will ever repair it. The
// mirror image, an object no row names, is cleaned up where it can be — a
// failed insert removes the object it just wrote — with one residual case: a
// put reported as failed after the object had landed leaves dead weight under
// a UUID key nothing refers to. Deleting it would cost every failing upload a
// second FileUploadTimeout against the storage that just failed, which is a
// worse trade than the dead weight.
//
// The error says how many files did commit: the caller reports the whole batch
// as failed, so without that count nothing records that some of it is durable.
func (s *Service) SaveMetadata(ctx context.Context, files []*SavedFile, ip string) error {
	if len(files) == 0 {
		return nil
	}

	for i, f := range files {
		if err := s.putObject(ctx, f); err != nil {
			return fmt.Errorf("%w (%d of %d files committed)", err, i, len(files))
		}
		if _, err := s.q.CreateUpload(ctx, sqlc.CreateUploadParams{
			Filepath:         f.key,
			OriginalFilename: f.originalFilename,
			Extension:        f.extension,
			Size:             int32(f.size), //nolint:gosec // size is capped at UploadMaxFileSize (5 MiB)
			Mimetype:         f.mimetype,
			UploaderIp:       ip,
		}); err != nil {
			s.removeObject(ctx, f)
			return fmt.Errorf("save upload metadata (%d of %d files committed): %w", i, len(files), err)
		}
	}
	logger.From(ctx).Info("files uploaded", "count", len(files))
	return nil
}

// putObject writes one buffered file to the object store under its key.
func (s *Service) putObject(ctx context.Context, f *SavedFile) error {
	putCtx, cancel := context.WithTimeout(ctx, config.FileUploadTimeout)
	defer cancel()

	opts := minio.PutObjectOptions{ContentType: f.mimetype}
	if _, err := s.store.PutObject(
		putCtx, s.bucket, f.key, bytes.NewReader(f.data), int64(len(f.data)), opts,
	); err != nil {
		return fmt.Errorf("put object %q: %w", f.key, err)
	}
	return nil
}
