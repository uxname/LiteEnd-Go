package db

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/config"
)

// TestC11_PoolSizeComesFromConfig covers C11: the pool size is the replica's own
// DB_POOL_MAX, not a compiled-in constant — sizing across replicas is an
// operator's call (replicas x DB_POOL_MAX < the Postgres limit), so it has to be
// reachable without a rebuild.
//
// The value is deliberately not 10: that is what the superseded constant held,
// so a pool still wired to it reports 10 here and this test fails.
func TestC11_PoolSizeComesFromConfig(t *testing.T) {
	t.Parallel()

	poolCfg, err := newPoolConfig(&config.Config{
		DatabaseHost:     "localhost",
		DatabasePort:     5432,
		DatabaseUser:     "postgres",
		DatabasePassword: "postgres",
		DatabaseName:     "postgres",
		DBPoolMax:        4,
	})
	require.NoError(t, err)
	require.Equal(t, int32(4), poolCfg.MaxConns, "pool size must come from Config.DBPoolMax")
}
