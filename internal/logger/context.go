package logger

import (
	"context"
	"log/slog"
)

// ctxKey is the private context key under which a request-scoped logger is
// stored. Using a zero-size struct key avoids collisions with other packages.
type ctxKey struct{}

// Into returns a copy of ctx carrying l, retrievable with From. A middleware
// builds a per-request logger (enriched with request_id, user_id, …) and stores
// it here so every log line within the request is correlated automatically.
func Into(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the request-scoped logger stored by Into, or the default logger
// when none is present (e.g. background paths or tests). Callers in request
// scope should always use this so their lines inherit request_id/user_id.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default() //nolint:sloglint // documented fallback when ctx carries no logger
}
