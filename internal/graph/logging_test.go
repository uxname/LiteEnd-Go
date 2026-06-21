package graph

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactVariables(t *testing.T) {
	t.Parallel()
	in := map[string]any{
		"username":      "alice",
		"password":      "hunter2",
		"Authorization": "Bearer xyz",
		"nested":        map[string]any{"ok": 1},
	}
	out := redactVariables(in)
	require.Equal(t, "alice", out["username"])
	require.Equal(t, "[REDACTED]", out["password"])
	require.Equal(t, "[REDACTED]", out["Authorization"], "redaction is case-insensitive")
	require.Equal(t, in["nested"], out["nested"])
}

func TestRedactVariables_Nil(t *testing.T) {
	t.Parallel()
	require.Nil(t, redactVariables(nil))
}

func TestRedactVariables_Recursive(t *testing.T) {
	t.Parallel()
	in := map[string]any{
		"input": map[string]any{
			"displayName": "alice",
			"token":       "deadbeef",
			"nested":      map[string]any{"secret": "s3cr3t"},
		},
		"list": []any{
			map[string]any{"password": "p"},
		},
	}
	out := redactVariables(in)
	inner, ok := out["input"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "alice", inner["displayName"])
	require.Equal(t, "[REDACTED]", inner["token"], "nested sensitive keys are redacted")

	nested, ok := inner["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "[REDACTED]", nested["secret"])

	list, ok := out["list"].([]any)
	require.True(t, ok)
	first, ok := list[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "[REDACTED]", first["password"])
}

func TestLoggableVariables_MutationDropsValues(t *testing.T) {
	t.Parallel()
	vars := map[string]any{"input": map[string]any{"displayName": "alice", "bio": "PII"}}
	require.Equal(t, "[REDACTED]", loggableVariables("mutation", vars),
		"mutation variable values must not be logged")
	// Queries still log (redacted) variables.
	require.IsType(t, map[string]any{}, loggableVariables("query", vars))
	require.Nil(t, loggableVariables("query", nil))
}
