// Package upload accepts multipart file uploads and stores them in an
// S3-compatible object store shared by every replica.
package upload

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// Writer is the subset of sqlc used to persist and read back upload metadata.
type Writer interface {
	CreateUpload(ctx context.Context, arg sqlc.CreateUploadParams) (sqlc.Upload, error)
	GetUploadByFilepath(ctx context.Context, filepath string) (sqlc.Upload, error)
}

// objectStore is the subset of the S3 client used to store uploaded objects.
// Every replica writes to the same bucket, so a file stored by one is readable
// through all the others.
type objectStore interface {
	PutObject(
		ctx context.Context, bucket, key string, r io.Reader, size int64, opts minio.PutObjectOptions,
	) (minio.UploadInfo, error)
	RemoveObject(ctx context.Context, bucket, key string, opts minio.RemoveObjectOptions) error
	// GetBucketLocation is asked once, to learn the region that goes into a
	// link's signature. Only the storage knows it ("garage", "us-east-1", a real
	// AWS region), and this client is the one that can reach it.
	GetBucketLocation(ctx context.Context, bucket string) (string, error)
}

// linkSigner is the subset of the S3 client used to sign download links. It is
// a SECOND client, bound to the address the browser uses, because a SigV4
// signature covers the host and path it was made for: signing with the internal
// endpoint (http://garage:3900) would produce links every browser is refused on.
type linkSigner interface {
	PresignedGetObject(
		ctx context.Context, bucket, key string, expiry time.Duration, params url.Values,
	) (*url.URL, error)
}

// Service stores uploaded files as objects and records their metadata.
type Service struct {
	q         Writer
	store     objectStore
	bucket    string
	publicURL string
	// public mirrors config.FilesArePublic: hand out the permanent link instead
	// of signing a short-lived one.
	public  bool
	linkTTL time.Duration

	// Link signing, built on first use. See signer().
	signerMu       sync.Mutex
	signer         linkSigner
	creds          *credentials.Credentials
	publicEndpoint string
	publicSecure   bool
}

// New builds an upload Service backed by the configured object store. A
// malformed storage endpoint stops the boot here rather than surfacing on the
// first upload — the composition root is the only place that can still refuse
// to start.
func New(cfg *config.Config, q Writer) (*Service, error) {
	endpoint, secure := parseEndpoint(cfg.S3Endpoint, cfg.S3UseSSL)
	creds := credentials.NewStaticV4(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, "")
	client, err := minio.New(endpoint, &minio.Options{Creds: creds, Secure: secure})
	if err != nil {
		return nil, fmt.Errorf("build object storage client: %w", err)
	}

	// The address the BROWSER uses, kept for the signing client built in signer().
	// TLS comes from this value's OWN scheme, never from S3_USE_SSL: that flag
	// describes the internal endpoint, and an https storage behind an http proxy
	// (or the reverse) is an ordinary deployment. Signing with the wrong scheme
	// hands the browser a link on a port nothing is listening on.
	publicEndpoint, publicSecure := parseEndpoint(cfg.S3PublicBaseURL, false)

	return &Service{
		q:              q,
		store:          client,
		bucket:         cfg.S3Bucket,
		publicURL:      cfg.S3PublicBaseURL,
		public:         cfg.FilesArePublic(),
		linkTTL:        cfg.FileLinkTTL(),
		creds:          creds,
		publicEndpoint: publicEndpoint,
		publicSecure:   publicSecure,
	}, nil
}

// linkSignerFor returns the client that signs download links, building it the
// first time one is needed.
//
// It is a SECOND client, bound to the address the browser uses, because a SigV4
// signature covers the host and path it was made for: signing with the internal
// endpoint (http://garage:3900) would produce links every browser is refused on.
// It is also built lazily and with an explicit Region, and both of those are the
// same fact — an S3 client with no region asks the endpoint for the bucket's
// location before it can sign, and this client's endpoint is the PUBLIC one,
// which from inside the network usually resolves to nothing (on the scale stand
// it was a connection refused to localhost:8080 on every upload). So the region
// is read once through the client that CAN reach the storage, and handed over.
func (s *Service) linkSignerFor(ctx context.Context) (linkSigner, error) {
	s.signerMu.Lock()
	signer := s.signer
	s.signerMu.Unlock()
	if signer != nil {
		return signer, nil
	}

	// The lookup runs OUTSIDE the lock, and under a timeout. Holding the mutex
	// across it would mean that a storage which is merely slow turns every
	// concurrent profile read into a queue, each waiting a full dial — a storage
	// outage would stall unrelated GraphQL reads instead of failing them. Racing
	// callers may each build a client; they are local objects that open no
	// connection, and only the first one is kept.
	lookupCtx, cancel := context.WithTimeout(ctx, config.FileUploadTimeout)
	defer cancel()
	region, err := s.store.GetBucketLocation(lookupCtx, s.bucket)
	if err != nil {
		return nil, fmt.Errorf("read storage region for %q: %w", s.bucket, err)
	}
	client, err := minio.New(s.publicEndpoint, &minio.Options{
		Creds:  s.creds,
		Secure: s.publicSecure,
		Region: region,
		// Path-style addressing, so the signed path is /<bucket>/<key> — exactly
		// the S3_PUBLIC_BASE_URL + "/" + key that config.validateFileLinks
		// enforces, and what a proxy in front of it must pass through unchanged.
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("build link-signing client: %w", err)
	}

	s.signerMu.Lock()
	defer s.signerMu.Unlock()
	if s.signer == nil {
		s.signer = client
	}
	return s.signer, nil
}

// LinkFor returns the URL a browser downloads the stored object from.
//
// In public mode that is the object's permanent link. In private mode (the
// default) it is a link signed for config.FileLinkTTL: the storage refuses the
// same URL without the signature, and refuses it again once the signature
// expires — so the link is handed out fresh on every read rather than stored.
func (s *Service) LinkFor(ctx context.Context, key string) (string, error) {
	if s.public {
		return s.PermanentLink(key), nil
	}
	signer, err := s.linkSignerFor(ctx)
	if err != nil {
		return "", err
	}
	signed, err := signer.PresignedGetObject(ctx, s.bucket, key, s.linkTTL, nil)
	if err != nil {
		return "", fmt.Errorf("sign link for %q: %w", key, err)
	}
	return signed.String(), nil
}

// OwnedBy reports whether the object under key was uploaded by this profile.
//
// It is the check that stops one signed-in user having a link signed for
// another user's file. A key is a claim, not a credential: it travels in every
// link we hand out, so by the time a client sends one back it may have been
// read off a screenshot, a chat message or an expired URL. An object nobody
// owns — a row from before owners were recorded, or one whose uploader was
// deleted — belongs to nobody and is refused too.
func (s *Service) OwnedBy(ctx context.Context, key string, profileID int32) (bool, error) {
	up, err := s.q.GetUploadByFilepath(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read upload %q: %w", key, err)
	}
	return up.UploaderProfileID != nil && *up.UploaderProfileID == profileID, nil
}

// PermanentLink is the object's stable address: the public link prefix plus the
// key. It is the form kept in the database — a value that names the object
// without expiring. In private mode the storage refuses this URL as it stands
// (that is the point); it is the key carrier that LinkFor signs on every read.
func (s *Service) PermanentLink(key string) string { return s.publicURL + "/" + key }

// KeyFromLink returns the object key a link of ours points at, and whether the
// link is one of ours at all. It is what turns a link the client hands back
// (fresh from an upload, signature and all) into the key behind it: a signed
// link expires, so storing one would store a dead value.
func (s *Service) KeyFromLink(link string) (string, bool) {
	key, found := strings.CutPrefix(link, s.publicURL+"/")
	if !found {
		return "", false
	}
	key, _, _ = strings.Cut(key, "?") // drop the signature query
	// The key arrives from a client and is about to be signed with OUR
	// credentials, so anything that is not a plain key under the bucket is
	// refused: minio-go accepts "../x" as an object name, and a proxy that
	// normalises the path would aim the request at a sibling bucket.
	//
	// Shape only — whether the key is THIS caller's is OwnedBy's question, asked
	// wherever a client hands a key back.
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
		return "", false
	}
	return key, true
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
// URL the browser downloads the object from — signed and short-lived unless the
// deployment runs in public file mode — and Key is the object's permanent name,
// the value to hand back to the API (e.g. as a profile avatar) so a fresh link
// can be issued on every read. Either way the browser loads the bytes straight
// from the storage, not through this app.
type SavedFile struct {
	Filename string `json:"filename"`
	Key      string `json:"key"`
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
// including the key and the download link it will get. The bytes are held until
// SaveMetadata stores them, so a request rejected while its parts are still
// being read leaves nothing in the bucket. It returns ErrDisallowedMime if the
// content-type is not allowed, and ErrFileTooLarge if the body exceeds
// UploadMaxFileSize.
//
// Storing the bytes happens in SaveMetadata; the only call out of here is the
// region lookup a signed link needs (see linkSignerFor) — made once per process
// on the success path, and retried by each caller while the storage is refusing
// it, so an unreachable storage fails an upload here before any bytes move.
func (s *Service) ProcessFile(
	ctx context.Context, originalFilename, mimetype string, body io.Reader,
) (*SavedFile, error) {
	// Cheap early reject on the client-declared content-type before reading on.
	if !AllowedMime(mimetype) {
		return nil, ErrDisallowedMime
	}

	// Content-based validation: peek at the leading bytes and trust the detected
	// type, not the client-supplied header (which is trivially spoofable). Peek
	// leaves them in the buffer, so the read below still gets the whole stream; a
	// file shorter than sniffLen ends the peek with io.EOF, which is not a failure.
	buffered := bufio.NewReader(body)
	head, err := buffered.Peek(sniffLen)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read upload head: %w", err)
	}
	detected := detectMime(head)
	if !AllowedMime(detected) {
		return nil, ErrDisallowedMime
	}
	// The body is buffered whole: it is capped at UploadMaxFileSize (5 MiB) per
	// file and at config.BodyLimit (10 MiB) per request, and PutObject stores a known
	// size in one atomic PUT instead of a multipart upload. Raising either cap
	// raises this memory ceiling with it.
	data, err := io.ReadAll(io.LimitReader(buffered, config.UploadMaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(data)) > config.UploadMaxFileSize {
		return nil, ErrFileTooLarge
	}

	key := objectKey(time.Now().UTC(), originalFilename)
	link, err := s.LinkFor(ctx, key)
	if err != nil {
		return nil, err
	}
	return &SavedFile{
		Filename:         path.Base(key),
		Key:              key,
		Path:             link,
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

// detectMime returns the sniffed MIME type without any charset parameters.
func detectMime(head []byte) string {
	detected := http.DetectContentType(head)
	if mt, _, err := mime.ParseMediaType(detected); err == nil {
		return mt
	}
	return detected
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
func (s *Service) SaveMetadata(ctx context.Context, files []*SavedFile, ip string, ownerID int32) error {
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
			// The owner, recorded so OwnedBy can answer later. /upload sits behind
			// requireAuth, so there is always one.
			UploaderProfileID: &ownerID,
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
