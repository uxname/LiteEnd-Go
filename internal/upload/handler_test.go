package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/logger"
)

// captureWriteErr runs writeErr with a capturing logger and returns the line.
func captureWriteErr(t *testing.T, code int, msg string, cause error) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	ctx := logger.Into(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))

	rec := httptest.NewRecorder()
	writeErr(ctx, rec, code, msg, cause)
	require.Equal(t, code, rec.Code)

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	return line
}

// The client-facing message is deliberately vague, so the reason has to reach
// the log — a 500 here used to leave nothing but a status code.
func TestWriteErr_ServerFaultLogsTheCause(t *testing.T) {
	t.Parallel()
	line := captureWriteErr(t, http.StatusInternalServerError, "Failed to save metadata",
		errors.New("connection refused"))

	require.Equal(t, "upload_error", line["msg"])
	require.Equal(t, slog.LevelError.String(), line["level"])
	require.Equal(t, "Failed to save metadata", line["reason"])
	require.Equal(t, "connection refused", line["error"])
}

// Rejected input is the caller's fault, not ours: Warn, and there may be no
// underlying error at all.
func TestWriteErr_RejectedInputWarnsWithoutACause(t *testing.T) {
	t.Parallel()
	line := captureWriteErr(t, http.StatusBadRequest, "Too many files", nil)

	require.Equal(t, slog.LevelWarn.String(), line["level"])
	require.Equal(t, "Too many files", line["reason"])
	require.NotContains(t, line, "error")
}
