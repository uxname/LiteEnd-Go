package upload

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/clientip"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/httperr"
	"github.com/uxname/liteend-go/internal/logger"
)

// Handler exposes the upload HTTP endpoint.
type Handler struct {
	svc *Service
}

// NewHandler builds an upload Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register mounts the routes. requireAuth wraps POST /upload. Stored files are
// served by the object store itself, so this app exposes no download route.
func (h *Handler) Register(r chi.Router, requireAuth func(http.Handler) http.Handler) {
	r.With(requireAuth).Post("/upload", h.upload)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(r.Context(), w, http.StatusBadRequest, "Request is not multipart", err)
		return
	}

	ip := clientip.ClientIP(r)
	saved := make([]*SavedFile, 0, config.UploadMaxFiles)
	fileCount := 0

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(r.Context(), w, http.StatusBadRequest, "Malformed multipart body", err)
			return
		}
		if part.FileName() == "" {
			continue // not a file part
		}

		fileCount++
		if fileCount > config.UploadMaxFiles {
			writeErr(r.Context(), w, http.StatusBadRequest, "Too many files", nil)
			return
		}

		// Enforce per-file size limit (read at most max+1 to detect overflow).
		limited := io.LimitReader(part, config.UploadMaxFileSize+1)
		f, err := h.svc.ProcessFile(r.Context(), part.FileName(), part.Header.Get("Content-Type"), limited)
		if errors.Is(err, ErrDisallowedMime) {
			continue // skip non-image parts
		}
		if errors.Is(err, ErrFileTooLarge) {
			writeErr(r.Context(), w, http.StatusBadRequest, "File too large", err)
			return
		}
		if err != nil {
			writeErr(r.Context(), w, http.StatusBadRequest, "Failed to process file", err)
			return
		}
		saved = append(saved, f)
	}

	if len(saved) == 0 {
		writeErr(r.Context(), w, http.StatusBadRequest, "No valid files uploaded", nil)
		return
	}

	// The route is mounted behind requireAuth, so a request without a user is a
	// wiring mistake rather than an anonymous caller — and one that would store
	// files nobody owns, which OwnedBy then refuses forever.
	user, err := auth.Require(r.Context())
	if err != nil {
		writeErr(r.Context(), w, http.StatusUnauthorized, "Not authenticated", err)
		return
	}

	if err := h.svc.SaveMetadata(r.Context(), saved, ip, user.ID); err != nil {
		// Nothing to roll back here: SaveMetadata commits per file and has already
		// undone the one it failed on. Files it committed before that stay stored
		// and recorded, and the client is still told the batch failed — so a retry
		// costs a duplicate object, never a broken one. A batch abandoned earlier
		// than this reached the object store at all (ProcessFile only buffers), so
		// the failure paths above have nothing to clean up either.
		writeErr(r.Context(), w, http.StatusInternalServerError, "Failed to save metadata", err)
		return
	}

	body, err := json.Marshal(saved)
	if err != nil {
		writeErr(r.Context(), w, http.StatusInternalServerError, "Failed to encode response", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body)
}

// writeErr answers the client and records why on the way out. The client-facing
// message is deliberately vague — this is a public endpoint taking user input —
// so without this line a 500 here left nothing behind but a status code. Server
// faults log at Error, rejected input at Warn, matching middleware.statusLevel.
func writeErr(ctx context.Context, w http.ResponseWriter, code int, msg string, cause error) {
	level := slog.LevelWarn
	if code >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	attrs := []slog.Attr{slog.Int("status", code), slog.String("reason", msg)}
	if cause != nil {
		attrs = append(attrs, slog.String("error", cause.Error()))
	}
	logger.From(ctx).LogAttrs(ctx, level, "upload_error", attrs...)
	httperr.Write(w, code, msg)
}
