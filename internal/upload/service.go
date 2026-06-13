// Package upload handles multipart file uploads and safe file serving.
package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// ErrForbidden is returned when a path escapes the upload root.
var ErrForbidden = errors.New("access denied")

// ErrNotFound is returned when a requested file does not exist.
var ErrNotFound = errors.New("file not found")

const defaultMime = "application/octet-stream"

var allowedMimeTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

// Writer is the subset of sqlc used to persist upload metadata.
type Writer interface {
	CreateUpload(ctx context.Context, arg sqlc.CreateUploadParams) (sqlc.Upload, error)
}

// Service stores uploaded files on disk and records metadata.
type Service struct {
	q         Writer
	log       *slog.Logger
	uploadDir string
}

// New builds an upload Service rooted at <cwd>/data/uploads.
func New(q Writer, log *slog.Logger) *Service {
	wd, _ := os.Getwd()
	return &Service{q: q, log: log, uploadDir: filepath.Join(wd, "data", "uploads")}
}

// SavedFile is the per-file result returned to the client.
type SavedFile struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
	// internal metadata (not serialised in the public response)
	filepath         string
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

// ProcessFile validates and writes a single uploaded file, returning its
// metadata, or nil if the mime type is not allowed.
func (s *Service) ProcessFile(ctx context.Context, originalFilename, mimetype string, body io.Reader) (*SavedFile, error) {
	if !AllowedMime(mimetype) {
		return nil, nil
	}

	relDir := relativeDir(time.Now().UTC())
	fullDir := filepath.Join(s.uploadDir, relDir)
	// 0o755: uploaded files are served publicly, and this lets the host user
	// browse ./data/uploads when bind-mounted.
	if err := os.MkdirAll(fullDir, 0o755); err != nil { //nolint:gosec // public upload dir
		return nil, fmt.Errorf("mkdir uploads: %w", err)
	}

	ext := filepath.Ext(originalFilename)
	name := uuid.NewString() + ext
	fullPath := filepath.Join(fullDir, name)

	writeCtx, cancel := context.WithTimeout(ctx, config.FileUploadTimeout)
	defer cancel()

	size, err := writeFile(writeCtx, fullPath, body)
	if err != nil {
		_ = os.Remove(fullPath) //nolint:gosec // content-addressed path under upload root
		return nil, err
	}

	return &SavedFile{
		Filename:         name,
		Path:             filepath.ToSlash(filepath.Join("/uploads", relDir, name)),
		filepath:         filepath.ToSlash(filepath.Join(relDir, name)),
		originalFilename: originalFilename,
		extension:        ext,
		size:             size,
		mimetype:         mimetype,
	}, nil
}

// SaveMetadata persists metadata for all uploaded files.
func (s *Service) SaveMetadata(ctx context.Context, files []*SavedFile, ip string) error {
	if len(files) == 0 {
		return nil
	}
	for _, f := range files {
		if _, err := s.q.CreateUpload(ctx, sqlc.CreateUploadParams{
			Filepath:         f.filepath,
			OriginalFilename: f.originalFilename,
			Extension:        f.extension,
			Size:             int32(f.size), //nolint:gosec // size is capped at UploadMaxFileSize (5 MiB)
			Mimetype:         f.mimetype,
			UploaderIp:       ip,
		}); err != nil {
			return fmt.Errorf("save upload metadata: %w", err)
		}
	}
	s.log.Info("files uploaded", "count", len(files))
	return nil
}

// SafeFileInfo resolves a request path to an absolute file path, rejecting any
// path that escapes the upload root (path-traversal protection).
func (s *Service) SafeFileInfo(relativePath string) (fullPath, mimeType string, err error) {
	root, err := filepath.Abs(s.uploadDir)
	if err != nil {
		return "", "", err
	}
	full := filepath.Join(root, filepath.Clean("/"+relativePath))
	resolved, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", "", ErrForbidden
	}
	if _, statErr := os.Stat(resolved); statErr != nil {
		return "", "", ErrNotFound
	}
	return resolved, mimeOf(resolved), nil
}

func mimeOf(path string) string {
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return t
	}
	return defaultMime
}

func relativeDir(t time.Time) string {
	return filepath.Join(
		fmt.Sprintf("%04d", t.Year()),
		fmt.Sprintf("%02d", int(t.Month())),
		fmt.Sprintf("%02d", t.Day()),
		fmt.Sprintf("%02d-%02d", t.Hour(), t.Minute()),
	)
}

func writeFile(ctx context.Context, path string, body io.Reader) (int64, error) {
	f, err := os.Create(path) //nolint:gosec // path is content-addressed under upload root
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer func() { _ = f.Close() }()

	done := make(chan struct{})
	var written int64
	var copyErr error
	go func() {
		written, copyErr = io.Copy(f, body)
		close(done)
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-done:
		return written, copyErr
	}
}
