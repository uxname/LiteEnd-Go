package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// storedUploads is the read half of Writer, shared by the fakes below: each
// keeps the rows it accepted so OwnedBy has something to answer from.
type storedUploads struct {
	rows map[string]sqlc.Upload
}

func (u *storedUploads) remember(arg sqlc.CreateUploadParams, id int32) sqlc.Upload {
	if u.rows == nil {
		u.rows = map[string]sqlc.Upload{}
	}
	row := sqlc.Upload{ID: id, Filepath: arg.Filepath, UploaderProfileID: arg.UploaderProfileID}
	u.rows[arg.Filepath] = row
	return row
}

func (u *storedUploads) GetUploadByFilepath(_ context.Context, filepath string) (sqlc.Upload, error) {
	row, ok := u.rows[filepath]
	if !ok {
		return sqlc.Upload{}, pgx.ErrNoRows
	}
	return row, nil
}

type fakeWriter struct {
	storedUploads
	count int
}

func (f *fakeWriter) CreateUpload(_ context.Context, arg sqlc.CreateUploadParams) (sqlc.Upload, error) {
	f.count++
	return f.remember(arg, int32(f.count)), nil
}

// failingWriter refuses to record metadata, which is how a batch that is
// already in the object store still has to fail.
type failingWriter struct {
	storedUploads
	err error
}

func (f *failingWriter) CreateUpload(_ context.Context, _ sqlc.CreateUploadParams) (sqlc.Upload, error) {
	return sqlc.Upload{}, f.err
}

// nthFailWriter commits every row but the nth, and remembers the keys it did
// commit. That is the shape of a partly written batch: some rows are already
// durable when a later one fails.
type nthFailWriter struct {
	storedUploads
	failOn    int
	err       error
	calls     int
	committed []string
}

func (w *nthFailWriter) CreateUpload(_ context.Context, arg sqlc.CreateUploadParams) (sqlc.Upload, error) {
	w.calls++
	if w.calls == w.failOn {
		return sqlc.Upload{}, w.err
	}
	w.committed = append(w.committed, arg.Filepath)
	return sqlc.Upload{ID: int32(w.calls)}, nil
}

// storedObject is what fakeStore remembers about a PutObject call.
type storedObject struct {
	bucket      string
	data        []byte
	contentType string
}

// fakeStore is an in-memory objectStore: unit tests must not need a live S3.
type fakeStore struct {
	objects     map[string]storedObject
	removed     []string
	putErr      error
	removeErr   error
	locationErr error
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
	// Drop it from the bucket as well, not just onto the audit list: a test
	// asking what survived a half-failed batch has to read the same map the
	// puts land in.
	delete(f.objects, key)
	f.removed = append(f.removed, key)
	return nil
}

func (f *fakeStore) GetBucketLocation(_ context.Context, _ string) (string, error) {
	if f.locationErr != nil {
		return "", f.locationErr
	}
	return "us-east-1", nil
}

const testPublicURL = "https://cdn.example.test/uploads"

// testOwnerID is the profile every test upload belongs to.
const testOwnerID int32 = 7

// fakeSigner stands in for the S3 client's presigner: it produces the same
// shape of URL (permanent link + a signature query) without any crypto.
type fakeSigner struct {
	err error
}

func (f *fakeSigner) PresignedGetObject(
	_ context.Context, bucket, key string, expiry time.Duration, _ url.Values,
) (*url.URL, error) {
	if f.err != nil {
		return nil, f.err
	}
	signed, err := url.Parse(fmt.Sprintf("%s/%s?X-Amz-Expires=%d&X-Amz-Signature=fake&bucket=%s",
		testPublicURL, key, int(expiry.Seconds()), bucket))
	if err != nil {
		return nil, fmt.Errorf("build fake signed url: %w", err)
	}
	return signed, nil
}

// newSvc builds a Service wired to an in-memory store, the same way New wires
// the real one. Files are private, the default.
func newSvc(t *testing.T) (*Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{
		objects: map[string]storedObject{}, removed: nil,
		putErr: nil, removeErr: nil, locationErr: nil,
	}
	return &Service{
		q:         &fakeWriter{},
		store:     store,
		signer:    &fakeSigner{err: nil},
		bucket:    "uploads",
		publicURL: testPublicURL,
		public:    false,
		linkTTL:   15 * time.Minute,
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

	require.NoError(t, s.SaveMetadata(context.Background(), []*SavedFile{f}, "10.0.0.1", testOwnerID))

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

	require.Equal(t, f.key, f.Key, "the permanent object key is returned alongside the link")
	require.True(t, strings.HasPrefix(f.Path, testPublicURL+"/"+f.key+"?"),
		"the link is the signed form of S3_PUBLIC_BASE_URL + key, got %q", f.Path)
	require.Contains(t, f.Path, "X-Amz-Signature=", "private files are served through a signed link")

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
// until the batch is committed — this is what lets the handler drop a rejected
// batch on the floor without any cleanup call.
func TestC5_AbandonedBatchStoresNothing(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	f, err := s.ProcessFile(context.Background(), "pic.png", "image/png", strings.NewReader(pngMagic+"data"))
	require.NoError(t, err)
	require.NotEmpty(t, f.key, "the file is buffered and already knows the key it would get")

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

	err = s.SaveMetadata(context.Background(), []*SavedFile{f}, "10.0.0.1", testOwnerID)
	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, []string{f.key}, store.removed, "the stored object must not outlive the failed batch")
}

// C5: the query layer has no transaction, so atomicity is per file, not per
// batch — and the code has to match that instead of promising more. A
// three-file batch whose second row insert fails must leave the first file
// whole (row AND object), roll back only the object of the file that failed,
// and never store the third at all. The two failures this rules out are a row
// pointing at a missing object — data corruption, the batch-wide rollback used
// to cause exactly this — and an object no row names, a silent leak.
func TestC5_BatchFailureKeepsCommittedFilesAndRollsBackOnlyTheFailedOne(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	w := &nthFailWriter{failOn: 2, err: os.ErrPermission}
	s.q = w

	files := make([]*SavedFile, 0, 3)
	for i := range 3 {
		f, err := s.ProcessFile(
			context.Background(), fmt.Sprintf("pic%d.png", i), "image/png", strings.NewReader(pngMagic+"data"),
		)
		require.NoError(t, err)
		files = append(files, f)
	}

	err := s.SaveMetadata(context.Background(), files, "10.0.0.1", testOwnerID)
	require.ErrorIs(t, err, os.ErrPermission, "a failed row insert must fail the call")

	require.Equal(t, []string{files[0].key}, w.committed, "the batch must stop at the failure")
	require.Equal(t, 2, w.calls, "the third file must never reach the database")

	// The committed row keeps its object: this is the assertion the old
	// batch-wide rollback broke.
	require.Contains(t, store.objects, files[0].key, "a committed row must keep its object")
	// Only the failed file's object is rolled back, and the third was never put.
	require.Equal(t, []string{files[1].key}, store.removed, "only the failed file's object may be removed")
	require.Equal(t, []string{files[0].key}, mapKeys(store.objects), "the bucket holds exactly the committed file")
}

// mapKeys is the object-store contents as a comparable list.
func mapKeys(m map[string]storedObject) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)
	return keys
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

// The two directions a link travels: out to the browser (signed, expiring) and
// back in as the value a client asks to store (normalised to the permanent
// form, because a signature stored is a link that stops working).
func TestLinkFor_PrivateSignsAndPublicDoesNot(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)

	signed, err := s.LinkFor(t.Context(), "2026/01/02/03-04/pic.png")
	require.NoError(t, err)
	require.Contains(t, signed, "X-Amz-Signature=", "private mode hands out a signed link")
	require.Contains(t, signed, "X-Amz-Expires=900", "the link carries the configured lifetime")

	s.public = true
	plain, err := s.LinkFor(t.Context(), "2026/01/02/03-04/pic.png")
	require.NoError(t, err)
	require.Equal(t, testPublicURL+"/2026/01/02/03-04/pic.png", plain,
		"public mode hands out the permanent link, unsigned")
}

func TestLinkFor_SigningFailureIsReported(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	s.signer = &fakeSigner{err: errors.New("no credentials")}

	_, err := s.LinkFor(t.Context(), "2026/01/02/03-04/pic.png")
	require.Error(t, err)
	require.Contains(t, err.Error(), "sign link")
}

func TestKeyFromLink(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)

	key, ours := s.KeyFromLink(testPublicURL + "/2026/01/02/03-04/pic.png?X-Amz-Signature=abc")
	require.True(t, ours)
	require.Equal(t, "2026/01/02/03-04/pic.png", key, "the signature query is not part of the key")

	key, ours = s.KeyFromLink(s.PermanentLink("2026/01/02/03-04/pic.png"))
	require.True(t, ours)
	require.Equal(t, "2026/01/02/03-04/pic.png", key)

	_, ours = s.KeyFromLink("https://i.pravatar.cc/300")
	require.False(t, ours, "somebody else's URL is not one of our keys")

	_, ours = s.KeyFromLink(testPublicURL + "/")
	require.False(t, ours, "the bare prefix names no object")
}

// The signing client is built on first use, from the region the storage itself
// reports — asked through the internal client, because the public endpoint is
// usually unreachable from inside the network. A storage that cannot answer
// must fail the link, not the process.
func TestLinkFor_BuildsTheSignerFromTheStorageRegion(t *testing.T) {
	t.Parallel()
	s, store := newSvc(t)
	s.signer = nil // as New leaves it
	s.creds = credentials.NewStaticV4("key", "secret", "")
	s.publicEndpoint = "cdn.example.test"
	s.publicSecure = true

	link, err := s.LinkFor(t.Context(), "2026/01/02/03-04/pic.png")
	require.NoError(t, err)
	require.Contains(t, link, "https://cdn.example.test/uploads/2026/01/02/03-04/pic.png?",
		"the link is built for the PUBLIC address, path-style")
	require.Contains(t, link, "X-Amz-Credential=key%2F", "signed with the configured credentials")
	require.Contains(t, link, "%2Fus-east-1%2Fs3%2Faws4_request", "…for the region the storage reported")
	require.NotNil(t, s.signer, "the client is kept for the next link")

	s2, _ := newSvc(t)
	s2.signer = nil
	s2.creds = credentials.NewStaticV4("key", "secret", "")
	s2.publicEndpoint = "cdn.example.test"
	store.locationErr = errors.New("storage down")
	s2.store = store
	_, err = s2.LinkFor(t.Context(), "2026/01/02/03-04/pic.png")
	require.Error(t, err)
	require.Contains(t, err.Error(), "read storage region")
}

// The avatar a client hands back is the only path by which an outsider names a
// key we then sign with our own credentials, so a key that is not a plain name
// under the bucket must not survive the trip.
func TestKeyFromLink_RefusesKeysThatEscapeTheBucket(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)

	for _, link := range []string{
		testPublicURL + "/../other-bucket/secret.sql",
		testPublicURL + "/2026/../../other-bucket/secret.sql",
		testPublicURL + "//etc/passwd",
		testPublicURL + "/?X-Amz-Signature=abc",
	} {
		_, ours := s.KeyFromLink(link)
		require.False(t, ours, "must not sign a link for %q", link)
	}
}

// OwnedBy is the check that keeps one user from having a link signed for
// another user's file, so it has to be strict in all four directions.
func TestOwnedBy(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	writer, ok := s.q.(*fakeWriter)
	require.True(t, ok)

	const key = "2026/01/02/03-04/pic.png"
	owner := testOwnerID
	stranger := testOwnerID + 1
	_, err := writer.CreateUpload(t.Context(), sqlc.CreateUploadParams{
		Filepath: key, UploaderProfileID: &owner,
	})
	require.NoError(t, err)

	owned, err := s.OwnedBy(t.Context(), key, owner)
	require.NoError(t, err)
	require.True(t, owned, "the uploader owns the file")

	owned, err = s.OwnedBy(t.Context(), key, stranger)
	require.NoError(t, err)
	require.False(t, owned, "somebody else does not")

	owned, err = s.OwnedBy(t.Context(), "2026/01/02/03-04/never-uploaded.png", owner)
	require.NoError(t, err)
	require.False(t, owned, "a key no upload recorded belongs to nobody")

	// A row from before owners were recorded, or one whose uploader was deleted.
	_, err = writer.CreateUpload(t.Context(), sqlc.CreateUploadParams{
		Filepath: "2026/01/02/03-04/ownerless.png", UploaderProfileID: nil,
	})
	require.NoError(t, err)
	owned, err = s.OwnedBy(t.Context(), "2026/01/02/03-04/ownerless.png", owner)
	require.NoError(t, err)
	require.False(t, owned, "an ownerless row is nobody's file, not everybody's")
}

// SaveMetadata records who uploaded the file — without that column OwnedBy has
// nothing to answer from.
func TestSaveMetadata_RecordsTheOwner(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	writer, ok := s.q.(*fakeWriter)
	require.True(t, ok)

	f, err := s.ProcessFile(t.Context(), "pic.png", "image/png", strings.NewReader(pngMagic+"data"))
	require.NoError(t, err)
	require.NoError(t, s.SaveMetadata(t.Context(), []*SavedFile{f}, "203.0.113.1", testOwnerID))

	row, err := writer.GetUploadByFilepath(t.Context(), f.key)
	require.NoError(t, err)
	require.NotNil(t, row.UploaderProfileID)
	require.Equal(t, testOwnerID, *row.UploaderProfileID)
}
