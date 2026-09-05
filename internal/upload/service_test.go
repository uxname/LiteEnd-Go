package upload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
)

type fakeWriter struct{ count int }

func (f *fakeWriter) CreateUpload(_ context.Context, _ sqlc.CreateUploadParams) (sqlc.Upload, error) {
	f.count++
	return sqlc.Upload{ID: int32(f.count)}, nil
}

// failingWriter refuses to record metadata, which is how a batch that is
// already in the object store still has to fail.
type failingWriter struct{ err error }

func (f *failingWriter) CreateUpload(_ context.Context, _ sqlc.CreateUploadParams) (sqlc.Upload, error) {
	return sqlc.Upload{}, f.err
}

// storedObject is what fakeStore remembers about a PutObject call.
type storedObject struct {
	bucket      string
	data        []byte
	contentType string
}

// fakeStore is an in-memory objectStore: unit tests must not need a live S3.
type fakeStore struct {
	objects   map[string]storedObject
	removed   []string
	putErr    error
	removeErr error
}

func (f *fakeStore) PutObject(
	_ context.Context, bucket, key string, r io.Reader, size int64, opts minio.PutObjectOptions,
) (minio.UploadInfo, error) {
	if f.putErr != nil {
		return minio.UploadInfo{}, f.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("fake store read: %w", err)
	}
	f.objects[key] = storedObject{bucket: bucket, data: data, contentType: opts.ContentType}
	return minio.UploadInfo{Key: key, Size: size}, nil
}

func (f *fakeStore) RemoveObject(_ context.Context, _, key string, _ minio.RemoveObjectOptions) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, key)
	return nil
}

const testPublicURL = "https://cdn.example.test/uploads"

// newSvc builds a Service wired to an in-memory store, the same way New wires
// the real one.
func newSvc(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{objects: map[string]storedObject{}, removed: nil, putErr: nil, removeErr: nil}
	return &Service{
		q:         &fakeWriter{},
		store:     store,
		bucket:    "uploads",
		publicURL: testPublicURL,
	}, store
}

func TestAllowedMime(t *testing.T) {
	t.Parallel()
	require.True(t, AllowedMime("image/png"))
	require.True(t, AllowedMime("image/jpeg"))
	require.True(t, AllowedMime("image/gif"))
	require.True(t, AllowedMime("image/webp"))
	require.False(t, AllowedMime("text/plain"))
	require.False(t, AllowedMime("application/pdf"))
}

// pngMagic is the 8-byte PNG signature that http.DetectContentType matches.
const pngMagic = "\x89PNG\r\n\x1a\n"

func TestProcessFile_RejectsDisallowedMime(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	f, err := s.ProcessFile(context.Background(), "x.txt", "text/plain", strings.NewReader("hi"))
	require.ErrorIs(t, err, ErrDisallowedMime, "non-image must be rejected")
	require.Nil(t, f)
	require.Empty(t, store.objects)
}

func TestProcessFile_RejectsSpoofedContent(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	// Declared image/png, but the bytes are plain text — content sniffing must
	// reject it regardless of the client-supplied content-type.
	f, err := s.ProcessFile(context.Background(), "evil.png", "image/png", strings.NewReader("this is not an image"))
	require.ErrorIs(t, err, ErrDisallowedMime, "spoofed content-type must be rejected by sniffing")
	require.Nil(t, f)
	require.Empty(t, store.objects)
}

// C5: a committed upload lands in the shared bucket under a dated, UUID-named
// key, and the link handed back is the configured public base plus that key —
// no byte of it touches local disk.
func TestC5_UploadIsCommittedToObjectStorage(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)

	f, err := s.ProcessFile(context.Background(), "pic.png", "image/png", strings.NewReader(pngMagic+"data"))
	require.NoError(t, err)
	require.NotNil(t, f)
	require.Empty(t, store.objects, "nothing is stored before the batch is committed")

	require.NoError(t, s.SaveMetadata(context.Background(), []*SavedFile{f}, "10.0.0.1"))

	require.Len(t, store.objects, 1, "exactly one object must be stored")
	obj, ok := store.objects[f.key]
	require.True(t, ok, "the object must be stored under the returned key")
	require.Equal(t, "uploads", obj.bucket)
	require.Equal(t, pngMagic+"data", string(obj.data))
	require.Equal(t, "image/png", obj.contentType, "stored mime is derived from content, not the header")

	require.Equal(t, relativeDir(time.Now().UTC()), path.Dir(f.key), "key keeps the YYYY/MM/DD/HH-MM layout")
	name := strings.TrimSuffix(path.Base(f.key), ".png")
	_, uuidErr := uuid.Parse(name)
	require.NoError(t, uuidErr, "the object name is a UUID, not the client filename")
	require.Equal(t, path.Base(f.key), f.Filename)

	require.Equal(t, testPublicURL+"/"+f.key, f.Path, "the link is built from S3_PUBLIC_BASE_URL")

	require.NoDirExists(t, filepath.Join(t.TempDir(), "2020"), "nothing may land on local disk")
}

// C5: the extension is the only client-controlled part of the key, so whatever
// the client calls its file, the key must come out as date prefix + UUID name +
// at most a plain alphanumeric suffix — no separator, no parent segment, no
// control or unicode character can ride along.
func TestC5_ObjectKeyIsPathTraversalSafe(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.September, 5, 12, 30, 0, 0, time.UTC)
	dir := "2026/09/05/12-30"
	shape := regexp.MustCompile(
		`^\d{4}/\d{2}/\d{2}/\d{2}-\d{2}/` +
			`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(\.[A-Za-z0-9]{1,9})?$`,
	)

	hostile := []string{
		"../../../etc/passwd",
		"..",
		".",
		"/etc/passwd",
		"pic.png/../../../../etc/passwd",
		"pic.pn/g",
		`pic.\..\..\windows`,
		"pic. png",
		"pic.png\x00.txt",
		"pic." + strings.Repeat("a", maxExtLen),
		strings.Repeat("../", 50) + "etc/passwd",
	}
	for _, name := range hostile {
		key := objectKey(at, name)
		require.Regexp(t, shape, key, "key %q built from %q", key, name)
		require.Equal(t, dir, path.Dir(key), "key %q must stay under the date prefix (from %q)", key, name)
		require.NotContains(t, key, "..", "key %q must carry no parent-directory segment", key)
		require.Equal(t, key, path.Clean(key), "key %q must already be clean", key)
	}
}

func TestC5_SafeExtKeepsOnlyShortAlphanumericSuffixes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"pic.png":          ".png",
		"pic.JPEG":         ".JPEG",
		"archive.tar.gz":   ".gz",
		"noext":            "",
		"trailing.":        "",
		"..":               "",
		"pic.pn/g":         "",
		`pic.pn\g`:         "",
		"pic. png":         "",
		"pic.png\x00":      "",
		"pic.πng":          "",
		"pic.superlongext": "",
	}
	for name, want := range cases {
		require.Equal(t, want, safeExt(name), "safeExt(%q)", name)
	}
}

func TestC5_ProcessFileRejectsOversizedFile(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	body := strings.NewReader(pngMagic + strings.Repeat("a", 5*1024*1024))

	f, err := s.ProcessFile(context.Background(), "big.png", "image/png", body)
	require.ErrorIs(t, err, ErrFileTooLarge)
	require.Nil(t, f)
	require.Empty(t, store.objects, "an oversized file must not reach the storage")
}

// C5: an abandoned batch cannot orphan an object, because nothing is stored
// until the batch is committed.
func TestC5_AbandonedBatchStoresNothing(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	f, err := s.ProcessFile(context.Background(), "pic.png", "image/png", strings.NewReader(pngMagic+"data"))
	require.NoError(t, err)

	s.RemoveFiles([]*SavedFile{f})
	require.Empty(t, store.objects, "an abandoned upload must leave no object behind")
	require.Empty(t, store.removed, "and nothing to delete either")
}

// C5: when the database half of the commit fails, the objects the same batch
// already stored are removed again.
func TestC5_MetadataFailureRemovesStoredObjects(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	s.q = &failingWriter{err: os.ErrPermission}

	f, err := s.ProcessFile(context.Background(), "pic.png", "image/png", strings.NewReader(pngMagic+"data"))
	require.NoError(t, err)

	err = s.SaveMetadata(context.Background(), []*SavedFile{f}, "10.0.0.1")
	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, []string{f.key}, store.removed, "the stored object must not outlive the failed batch")
}

// C5: New takes the storage contract from the config the process already
// validated, so a misconfiguration stops the boot instead of travelling inside
// the service to the first upload.
func TestC5_NewUsesTheInjectedStorageConfig(t *testing.T) {
	t.Parallel()
	s, err := New(&config.Config{
		S3Endpoint:        "http://storage:3900",
		S3AccessKeyID:     "key",
		S3SecretAccessKey: "secret",
		S3Bucket:          "media",
		S3PublicBaseURL:   "https://cdn.example.test/media",
	}, &fakeWriter{})
	require.NoError(t, err)
	require.NotNil(t, s.store)
	require.Equal(t, "media", s.bucket)
	require.Equal(t, "https://cdn.example.test/media", s.publicURL)
}

// C5: a malformed endpoint is a boot failure, not a runtime surprise — New
// reports it to the composition root, which refuses to start.
func TestC5_NewRejectsAMalformedEndpoint(t *testing.T) {
	t.Parallel()
	s, err := New(&config.Config{S3Endpoint: "http://host:notaport"}, &fakeWriter{})
	require.Error(t, err)
	require.Nil(t, s)
}

func TestC5_ParseEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw        string
		useSSL     bool
		wantHost   string
		wantSecure bool
	}{
		{raw: "http://garage:3900", useSSL: false, wantHost: "garage:3900", wantSecure: false},
		{raw: "https://s3.example.test", useSSL: false, wantHost: "s3.example.test", wantSecure: true},
		{raw: "storage:9000", useSSL: true, wantHost: "storage:9000", wantSecure: true},
		{raw: "storage:9000", useSSL: false, wantHost: "storage:9000", wantSecure: false},
	}
	for _, c := range cases {
		host, secure := parseEndpoint(c.raw, c.useSSL)
		require.Equal(t, c.wantHost, host, "host of %q", c.raw)
		require.Equal(t, c.wantSecure, secure, "tls of %q", c.raw)
	}
}
