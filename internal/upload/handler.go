package upload

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/httperr"
	"github.com/uxname/liteend-go/internal/logger"
)

// Handler exposes the upload/download HTTP endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds an upload Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Register mounts the routes. requireAuth wraps POST /upload.
func (h *Handler) Register(r chi.Router, requireAuth func(http.Handler) http.Handler) {
	r.With(requireAuth).Post("/upload", h.upload)
	r.Get("/uploads/*", h.serve)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(r.Context(), w, http.StatusBadRequest, "Request is not multipart", err)
		return
	}

	ip := clientIP(r)
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
			h.svc.RemoveFiles(saved)
			writeErr(r.Context(), w, http.StatusBadRequest, "File too large", err)
			return
		}
		if err != nil {
			h.svc.RemoveFiles(saved)
			writeErr(r.Context(), w, http.StatusBadRequest, "Failed to process file", err)
			return
		}
		saved = append(saved, f)
	}

	if len(saved) == 0 {
		writeErr(r.Context(), w, http.StatusBadRequest, "No valid files uploaded", nil)
		return
	}

	if err := h.svc.SaveMetadata(r.Context(), saved, ip); err != nil {
		h.svc.RemoveFiles(saved) // roll back orphaned files when metadata fails
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

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	rel := chi.URLParam(r, "*")
	fullPath, mimeType, err := h.svc.SafeFileInfo(rel)
	switch {
	case errors.Is(err, ErrForbidden):
		writeErr(r.Context(), w, http.StatusForbidden, "Access denied", err)
		return
	case errors.Is(err, ErrNotFound):
		writeErr(r.Context(), w, http.StatusNotFound, "File not found", err)
		return
	case err != nil:
		writeErr(r.Context(), w, http.StatusInternalServerError, "Internal error", err)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	http.ServeFile(w, r, fullPath)
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

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
