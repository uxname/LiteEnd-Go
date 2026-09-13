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
func newTestLogger(buf *bytes.Buffer, level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level:       parseLevel(level),
		ReplaceAttr: redactSensitive,
	}))
}

// logLine logs one message through the handler and returns the decoded JSON.
func logLine(t *testing.T, attrs ...any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	newTestLogger(&buf, "info").Info("msg", attrs...)
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

func TestParseLevel(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]slog.Level{
		"debug":    slog.LevelDebug,
		"trace":    slog.LevelDebug,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"fatal":    slog.LevelError,
		"":         slog.LevelInfo,
		"nonsense": slog.LevelInfo,
	} {
		require.Equal(t, want, parseLevel(in), "level %q", in)
	}
	require.Equal(t, slog.LevelWarn, parseLevel("  WARN  "), "surrounding whitespace is trimmed")
	require.True(t, New("debug").Enabled(context.Background(), slog.LevelDebug))
}

func TestContextRoundTrip(t *testing.T) {
	t.Parallel()
	l := New("info")
	require.Same(t, l, From(Into(context.Background(), l)))
	require.Same(t, slog.Default(), From(context.Background()), "no logger in ctx falls back to the default")
}
