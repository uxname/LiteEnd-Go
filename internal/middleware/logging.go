// Package middleware holds HTTP middleware: request-id, recovery, logging,
// security headers, compression, and rate limiting.
package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/uxname/liteend-go/internal/logger"
)

// ContextLogger stores a per-request logger (base enriched with request_id) in
// the request context, so domain/service logs retrieved via logger.From(ctx)
// are correlated to the request. Must run after chi's RequestID middleware.
func ContextLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqLog := base.With(slog.String("request_id", middleware.GetReqID(r.Context())))
			next.ServeHTTP(w, r.WithContext(logger.Into(r.Context(), reqLog)))
		})
	}
}

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
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("remote", r.RemoteAddr),
			)
		})
	}
}
