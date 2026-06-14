package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/uxname/liteend-go/internal/httperr"
)

// Recoverer catches panics, logs them with the request id and stack, and
// returns a 500. Mirrors the TS uncaughtException safety net at request scope.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel comparison per net/http
						panic(rec)
					}
					log.LogAttrs(
						r.Context(), slog.LevelError, "panic_recovered",
						slog.Any("panic", rec),
						slog.String("request_id", middleware.GetReqID(r.Context())),
						slog.String("stack", string(debug.Stack())),
					)
					httperr.Write(w, http.StatusInternalServerError, "Internal Server Error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps request body size (mirrors Fastify bodyLimit).
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
