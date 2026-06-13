// Package middleware holds HTTP middleware: request-id, recovery, logging,
// security headers, compression, and rate limiting.
package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger logs each request with method, path, status, duration and
// request id using slog.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			log.LogAttrs(
				r.Context(), slog.LevelInfo, "http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.String("duration", time.Since(start).String()),
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("remote", r.RemoteAddr),
			)
		})
	}
}
