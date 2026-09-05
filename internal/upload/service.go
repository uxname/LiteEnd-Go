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
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/logger"
)

// ErrForbidden is returned when a path escapes the upload root.
var ErrForbidden = errors.New("access denied")

// ErrNotFound is returned when a requested file does not exist.
var ErrNotFound = errors.New("file not found")

// ErrDisallowedMime is returned by ProcessFile when the uploaded content-type is
// not in the image allowlist. Callers skip such files rather than failing.
var ErrDisallowedMime = errors.New("disallowed mime type")

// ErrFileTooLarge is returned by ProcessFile when the uploaded file exceeds
// UploadMaxFileSize. Nothing is stored before returning.
var ErrFileTooLarge = errors.New("file too large")

const defaultMime = "application/octet-stream"

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
	// initErr carries a storage misconfiguration to the first upload that needs
	// storage: New has no way to report one, so the error travels to the caller
	// (and from there into the log) instead of being dropped.
	initErr error
	// uploadDir is the local root SafeFileInfo resolves request paths against.
	uploadDir string
}

// New builds an upload Service backed by the configured object store.
//
// The storage settings are read from the environment — the same values the
// process validated at startup — because the composition root builds this
// service from the query set alone. A missing variable therefore still stops the
// boot (config.Load requires it); a malformed endpoint surfaces on first upload.
func New(q Writer) *Service {
	wd, _ := os.Getwd()
	svc := &Service{uploadDir: filepath.Join(wd, "data", "uploads"), q: q}

	cfg, err := config.Load()
	if err != nil {
		svc.initErr = fmt.Errorf("load storage config: %w", err)
		return svc
	}

	endpoint, secure := parseEndpoint(cfg.S3Endpoint, cfg.S3UseSSL)
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, ""),
		Secure: secure,
	})
	if err != nil {
		svc.initErr = fmt.Errorf("build object storage client: %w", err)
		return svc
	}

	svc.store = client
	svc.bucket = cfg.S3Bucket
	svc.publicURL = cfg.S3PublicBaseURL
	return svc
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
// SaveMetadata commits the batch, so a request that fails half-way leaves
// nothing in the bucket to clean up. It returns ErrDisallowedMime if the
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
	if s.initErr != nil {
		return nil, s.initErr
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

// RemoveFiles discards a batch that will not be persisted. Nothing reaches the
// object store before SaveMetadata, so an abandoned upload leaves no object
// behind — this only drops the bytes the batch was holding.
func (s *Service) RemoveFiles(files []*SavedFile) {
	for _, f := range files {
		f.data = nil
	}
}

// removeObjects deletes the objects a failed batch already stored. It detaches
// from ctx first: the failure being cleaned up may be that very cancellation.
func (s *Service) removeObjects(ctx context.Context, files []*SavedFile) {
	if len(files) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.FileUploadTimeout)
	defer cancel()

	opts := minio.RemoveObjectOptions{}
	for _, f := range files {
		if err := s.store.RemoveObject(ctx, s.bucket, f.key, opts); err != nil {
			// Left behind, the object is unreferenced but still stored, so its key
			// has to reach the log — this line is the only trace of it.
			logger.From(ctx).Warn("upload rollback failed", "key", f.key, "error", err.Error())
		}
	}
}

// SaveMetadata commits a validated batch: every file is stored as an object and
// then recorded in the database. The two move together — if either half fails,
// the objects this batch wrote are removed again, so a failed upload leaves
// nothing orphaned in the bucket.
func (s *Service) SaveMetadata(ctx context.Context, files []*SavedFile, ip string) error {
	if len(files) == 0 {
		return nil
	}

	stored := make([]*SavedFile, 0, len(files))
	for _, f := range files {
		if err := s.putObject(ctx, f); err != nil {
			s.removeObjects(ctx, stored)
			return err
		}
		stored = append(stored, f)
	}

	for _, f := range files {
		if _, err := s.q.CreateUpload(ctx, sqlc.CreateUploadParams{
			Filepath:         f.key,
			OriginalFilename: f.originalFilename,
			Extension:        f.extension,
			Size:             int32(f.size), //nolint:gosec // size is capped at UploadMaxFileSize (5 MiB)
			Mimetype:         f.mimetype,
			UploaderIp:       ip,
		}); err != nil {
			s.removeObjects(ctx, stored)
			return fmt.Errorf("save upload metadata: %w", err)
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

// SafeFileInfo resolves a request path to an absolute file path, rejecting any
// path that escapes the upload root (path-traversal protection).
func (s *Service) SafeFileInfo(relativePath string) (fullPath, mimeType string, err error) {
	root, err := filepath.Abs(s.uploadDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve upload root: %w", err)
	}
	full := filepath.Join(root, filepath.Clean("/"+relativePath))
	resolved, err := filepath.Abs(full)
	if err != nil {
		return "", "", fmt.Errorf("resolve upload path: %w", err)
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", "", ErrForbidden
	}
	if _, statErr := os.Stat(resolved); statErr != nil { //nolint:gosec // G703: the check above proves resolved is inside root
		return "", "", ErrNotFound
	}
	return resolved, mimeOf(resolved), nil
}

func mimeOf(name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return defaultMime
}
