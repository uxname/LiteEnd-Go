package upload

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/db/sqlc"
)

type fakeWriter struct{ count int }

func (f *fakeWriter) CreateUpload(_ context.Context, _ sqlc.CreateUploadParams) (sqlc.Upload, error) {
	f.count++
	return sqlc.Upload{ID: int32(f.count)}, nil
}

func newSvc(t *testing.T) *Service {
	t.Helper()
	s := New(&fakeWriter{}, slog.New(slog.DiscardHandler))
	s.uploadDir = t.TempDir()
	return s
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

func TestProcessFile_RejectsDisallowedMime(t *testing.T) {
	t.Parallel()
	s := newSvc(t)
	f, err := s.ProcessFile(context.Background(), "x.txt", "text/plain", strings.NewReader("hi"))
	require.ErrorIs(t, err, ErrDisallowedMime, "non-image must be rejected")
	require.Nil(t, f)
}

func TestProcessFile_WritesImage(t *testing.T) {
	t.Parallel()
	s := newSvc(t)
	f, err := s.ProcessFile(context.Background(), "pic.png", "image/png", strings.NewReader("\x89PNGdata"))
	require.NoError(t, err)
	require.NotNil(t, f)
	require.True(t, strings.HasSuffix(f.Filename, ".png"))
	require.True(t, strings.HasPrefix(f.Path, "/uploads/"))
	// file exists on disk
	abs := filepath.Join(s.uploadDir, f.filepath)
	_, statErr := os.Stat(abs)
	require.NoError(t, statErr)
}

func TestSafeFileInfo_PathTraversalBlocked(t *testing.T) {
	t.Parallel()
	s := newSvc(t)
	// The path is clamped under the upload root (filepath.Clean), so a host file
	// is never reachable: the result is an error (Forbidden or NotFound), and the
	// resolved path — if any — never escapes the root.
	full, _, err := s.SafeFileInfo("../../../etc/passwd")
	require.Error(t, err)
	require.Empty(t, full, "must not resolve to a host path")
}

func TestSafeFileInfo_NotFound(t *testing.T) {
	t.Parallel()
	s := newSvc(t)
	_, _, err := s.SafeFileInfo("2026/06/13/12-00/missing.png")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSafeFileInfo_Valid(t *testing.T) {
	t.Parallel()
	s := newSvc(t)
	f, err := s.ProcessFile(context.Background(), "ok.png", "image/png", strings.NewReader("data"))
	require.NoError(t, err)
	full, mime, err := s.SafeFileInfo(f.filepath)
	require.NoError(t, err)
	require.Equal(t, "image/png", mime)
	require.True(t, strings.HasPrefix(full, s.uploadDir))
}
