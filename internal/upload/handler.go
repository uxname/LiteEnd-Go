package upload

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/httperr"
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
		writeErr(w, http.StatusBadRequest, "Request is not multipart")
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
			writeErr(w, http.StatusBadRequest, "Malformed multipart body")
			return
		}
		if part.FileName() == "" {
			continue // not a file part
		}

		fileCount++
		if fileCount > config.UploadMaxFiles {
			writeErr(w, http.StatusBadRequest, "Too many files")
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
			writeErr(w, http.StatusBadRequest, "File too large")
			return
		}
		if err != nil {
			h.svc.RemoveFiles(saved)
			writeErr(w, http.StatusBadRequest, "Failed to process file")
			return
		}
		saved = append(saved, f)
	}

	if len(saved) == 0 {
		writeErr(w, http.StatusBadRequest, "No valid files uploaded")
		return
	}

	if err := h.svc.SaveMetadata(r.Context(), saved, ip); err != nil {
		h.svc.RemoveFiles(saved) // roll back orphaned files when metadata fails
		writeErr(w, http.StatusInternalServerError, "Failed to save metadata")
		return
	}

	body, err := json.Marshal(saved)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Failed to encode response")
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
		writeErr(w, http.StatusForbidden, "Access denied")
		return
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "File not found")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "Internal error")
		return
	}
	w.Header().Set("Content-Type", mimeType)
	http.ServeFile(w, r, fullPath)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
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
