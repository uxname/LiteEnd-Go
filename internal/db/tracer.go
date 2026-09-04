package db

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
)

// slowQueryTracer logs queries slower than config.DBSlowQueryThreshold and every
// query that fails. Successful fast queries are not logged at all: per-query
// logging is the fastest way to make a log unreadable, while "the site is slow"
// with no DB signal at all is undiagnosable. Arguments are never logged — they
// routinely carry PII and secrets, and pgx gives them to us untyped, where the
// slog redaction by key cannot help.
type slowQueryTracer struct{}

type traceStartKey struct{}

// traceStart is what TraceQueryStart hands to TraceQueryEnd: pgx only reports
// the SQL on the start side, and only the error on the end side.
type traceStart struct {
	at  time.Time
	sql string
}

// TraceQueryStart records the query's start time and SQL on the context pgx
// threads through to TraceQueryEnd.
func (slowQueryTracer) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	return context.WithValue(ctx, traceStartKey{}, traceStart{at: time.Now(), sql: data.SQL})
}

// TraceQueryEnd logs the query when it failed or ran long.
func (slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	started, ok := ctx.Value(traceStartKey{}).(traceStart)
	if !ok {
		return
	}
	elapsed := time.Since(started.at)

	// The request-scoped logger, so a slow query carries the request_id of
	// whoever caused it; on background paths logger.From falls back to the
	// default logger, which main installs as the same JSON handler.
	log := logger.From(ctx)

	switch {
	case data.Err != nil:
		log.LogAttrs(ctx, slog.LevelError, "db_query_failed",
			slog.String("sql", started.sql),
			slog.Int64("duration_ms", elapsed.Milliseconds()),
			slog.String("error", data.Err.Error()))
	case elapsed >= config.DBSlowQueryThreshold:
		log.LogAttrs(ctx, slog.LevelWarn, "db_query_slow",
			slog.String("sql", started.sql),
			slog.Int64("duration_ms", elapsed.Milliseconds()))
	}
}
