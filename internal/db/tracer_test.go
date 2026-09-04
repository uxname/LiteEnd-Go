package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
)

// traceQuery runs one traced query of the given duration/outcome and returns the
// decoded log line, or nil when nothing was logged.
func traceQuery(t *testing.T, sql string, took time.Duration, queryErr error) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	ctx := logger.Into(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))

	tr := slowQueryTracer{}
	ctx = tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: sql})
	// Rewind the recorded start so the query "took" the requested duration
	// without the test actually sleeping for it.
	started, ok := ctx.Value(traceStartKey{}).(traceStart)
	require.True(t, ok)
	started.at = started.at.Add(-took)
	ctx = context.WithValue(ctx, traceStartKey{}, started)

	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: queryErr})

	if buf.Len() == 0 {
		return nil
	}
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	return line
}

func TestSlowQueryTracer_FastQueryIsSilent(t *testing.T) {
	t.Parallel()
	require.Nil(t, traceQuery(t, "SELECT 1", time.Millisecond, nil),
		"logging every query is the fastest way to make a log unreadable")
}

func TestSlowQueryTracer_SlowQueryWarns(t *testing.T) {
	t.Parallel()
	line := traceQuery(t, "SELECT pg_sleep(1)", config.DBSlowQueryThreshold+time.Second, nil)
	require.NotNil(t, line)
	require.Equal(t, "db_query_slow", line["msg"])
	require.Equal(t, slog.LevelWarn.String(), line["level"])
	require.Equal(t, "SELECT pg_sleep(1)", line["sql"])
	require.Positive(t, line["duration_ms"])
}

func TestSlowQueryTracer_FailedQueryErrors(t *testing.T) {
	t.Parallel()
	line := traceQuery(t, "SELECT bad", time.Millisecond, errors.New("syntax error"))
	require.NotNil(t, line, "a failed query is logged however fast it failed")
	require.Equal(t, "db_query_failed", line["msg"])
	require.Equal(t, slog.LevelError.String(), line["level"])
	require.Equal(t, "syntax error", line["error"])
}
