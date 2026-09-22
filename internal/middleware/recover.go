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
			ctx := r.Context()
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel comparison per net/http
						panic(rec) //nolint:forbidigo // re-raise ErrAbortHandler so net/http aborts the connection
					}
					// method/path are on the line itself: this middleware sits
					// inside RequestLogger, but a reader grepping for panics should
					// not have to join two lines to learn which URL blew up.
					log.LogAttrs(
						ctx, slog.LevelError, "panic_recovered",
						slog.Any("panic", rec),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.String("request_id", middleware.GetReqID(ctx)),
						slog.String("stack", string(debug.Stack())),
					)
					httperr.Write(w, http.StatusInternalServerError, "Internal Server Error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
