package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGet_ReturnsBuildMetadata(t *testing.T) {
	t.Parallel()
	info := Get()
	require.Equal(t, AppName, info.Name)
	require.Equal(t, AppVersion, info.Version)
	require.Equal(t, Commit, info.Commit)
	require.Equal(t, BuildTime, info.BuildTime)
}
