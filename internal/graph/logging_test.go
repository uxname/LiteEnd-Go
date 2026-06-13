package graph

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactVariables(t *testing.T) {
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
	require.Nil(t, redactVariables(nil))
}
