package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/logger"
)

// captureErrorLog runs the presenter with a capturing logger in ctx and returns
// the single decoded log line it wrote.
func captureErrorLog(t *testing.T, isProd bool, err error) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	ctx := logger.Into(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	_ = newErrorPresenter(isProd)(ctx, err)

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	return line
}

func TestErrorPresenter_Unauthenticated(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(false)(context.Background(), auth.ErrUnauthenticated)
	require.Equal(t, "UNAUTHENTICATED", gqlErr.Extensions["code"])
	require.Equal(t, 401, gqlErr.Extensions["statusCode"])
	require.NotEmpty(t, gqlErr.Extensions["requestId"])
}

func TestErrorPresenter_Forbidden(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(false)(context.Background(), auth.ErrForbidden)
	require.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
	require.Equal(t, 403, gqlErr.Extensions["statusCode"])
}

func TestErrorPresenter_GenericIsInternal(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(false)(context.Background(), errors.New("boom"))
	require.Equal(t, "INTERNAL_SERVER_ERROR", gqlErr.Extensions["code"])
	require.Equal(t, 500, gqlErr.Extensions["statusCode"])
	require.Equal(t, "boom", gqlErr.Message, "dev mode keeps the original message")
}

func TestErrorPresenter_ProductionMasksInternal(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(true)(context.Background(), errors.New("connection string leak"))
	require.Equal(t, "INTERNAL_SERVER_ERROR", gqlErr.Extensions["code"])
	require.Equal(t, genericInternalMessage, gqlErr.Message, "prod masks internal error text")
}

func TestErrorPresenter_ProductionKeepsAuthMessage(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(true)(context.Background(), auth.ErrUnauthenticated)
	require.Equal(t, "UNAUTHENTICATED", gqlErr.Extensions["code"])
	require.NotEqual(t, genericInternalMessage, gqlErr.Message, "client-safe errors are not masked")
}

// GraphQL always answers HTTP 200, so the access log cannot see a failed
// operation: every error has to leave its own line, or it leaves no trace.
func TestErrorPresenter_LogsInternalErrorAtErrorLevel(t *testing.T) {
	t.Parallel()
	line := captureErrorLog(t, false, errors.New("boom"))
	require.Equal(t, "graphql_error", line["msg"])
	require.Equal(t, slog.LevelError.String(), line["level"])
	require.Equal(t, "boom", line["error"])
	require.Equal(t, codeInternal, line["code"])
	require.NotEmpty(t, line["request_id"])
}

// A client fault is not an incident: it is logged, but at Warn.
func TestErrorPresenter_LogsClientErrorAtWarnLevel(t *testing.T) {
	t.Parallel()
	line := captureErrorLog(t, false, auth.ErrForbidden)
	require.Equal(t, "graphql_error", line["msg"])
	require.Equal(t, slog.LevelWarn.String(), line["level"])
	require.Equal(t, "FORBIDDEN", line["code"])
}

// Production masks the message in the *response*; the log must still hold the
// original text, or a masked 500 becomes undiagnosable.
func TestErrorPresenter_ProductionLogsUnmaskedMessage(t *testing.T) {
	t.Parallel()
	line := captureErrorLog(t, true, errors.New("connection string leak"))
	require.Equal(t, "connection string leak", line["error"],
		"the log keeps the original message the client never sees")
}
