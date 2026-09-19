package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestLogger mirrors New but writes to buf, so the handler under test —
// including its ReplaceAttr hook — is the one the app really installs.
func newTestLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactSensitive,
	}))
}

// logLine logs one message through the handler and returns the decoded JSON.
func logLine(t *testing.T, attrs ...any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	newTestLogger(&buf, slog.LevelInfo).Info("msg", attrs...)
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	return line
}

func TestRedactSensitive_TopLevelKeys(t *testing.T) {
	t.Parallel()
	line := logLine(t,
		"user", "alice",
		"password", "hunter2",
		"Authorization", "Bearer xyz",
	)
	require.Equal(t, "alice", line["user"])
	require.Equal(t, Redacted, line["password"])
	require.Equal(t, Redacted, line["Authorization"], "redaction is case-insensitive")
}

func TestRedactSensitive_NestedValues(t *testing.T) {
	t.Parallel()
	line := logLine(t, "input", map[string]any{
		"displayName": "alice",
		"token":       "deadbeef",
		"nested":      map[string]any{"secret": "s3cr3t"},
		"list":        []any{map[string]any{"cookie": "c"}},
	})
	input, ok := line["input"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "alice", input["displayName"])
	require.Equal(t, Redacted, input["token"])

	nested, ok := input["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, Redacted, nested["secret"])

	list, ok := input["list"].([]any)
	require.True(t, ok)
	first, ok := list[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, Redacted, first["cookie"], "sensitive keys inside slices are redacted")
}

func TestRedactSensitive_NilAnyValueSurvives(t *testing.T) {
	t.Parallel()
	line := logLine(t, "payload", nil)
	require.Contains(t, line, "payload")
	require.Nil(t, line["payload"])
}

func TestSensitiveKeyAndRedactValue(t *testing.T) {
	t.Parallel()
	require.True(t, SensitiveKey("SECRET"))
	require.False(t, SensitiveKey("username"))

	in := map[string]any{"sig": "abc", "keep": 1}
	out, ok := RedactValue(in).(map[string]any)
	require.True(t, ok)
	require.Equal(t, Redacted, out["sig"])
	require.Equal(t, 1, out["keep"])
	require.Equal(t, "abc", in["sig"], "the input map is not mutated")
	require.Equal(t, 42, RedactValue(42), "non-container values pass through")
}

// New wires the level it is handed into the handler. Parsing LOG_LEVEL is not
// tested here because it is not done here: config reads it into a slog.Level
// (see TestLoad_LogLevel), so this package never sees the raw string.
func TestNew_HonoursTheLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	require.True(t, New(slog.LevelDebug).Enabled(ctx, slog.LevelDebug))
	require.False(t, New(slog.LevelWarn).Enabled(ctx, slog.LevelInfo), "below the level is dropped")
	require.True(t, New(slog.LevelWarn).Enabled(ctx, slog.LevelError))
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()
	l := New(slog.LevelInfo)
	require.Same(t, l, From(Into(context.Background(), l)))
	require.Same(t, slog.Default(), From(context.Background()), "no logger in ctx falls back to the default")
}
