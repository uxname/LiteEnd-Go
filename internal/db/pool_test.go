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

// TestPoolConfig_SurvivesSpecialCharactersInThePassword pins the connection URL
// to net/url: a generated password routinely holds "/", "#", "?" or "%", and a
// URL assembled with Sprintf breaks on every one of them — the boot then dies on
// a parse error that never mentions the password.
func TestPoolConfig_SurvivesSpecialCharactersInThePassword(t *testing.T) {
	t.Parallel()

	for _, password := range []string{"p/w", "p#w", "p?w", "p%41w", "p@w:x", "p w"} {
		poolCfg, err := newPoolConfig(&config.Config{
			DatabaseHost:     "db.internal",
			DatabasePort:     6543,
			DatabaseUser:     "app",
			DatabasePassword: password,
			DatabaseName:     "shop",
			DBPoolMax:        4,
		})
		require.NoError(t, err, "password %q", password)
		require.Equal(t, password, poolCfg.ConnConfig.Password, "password %q", password)
		require.Equal(t, "app", poolCfg.ConnConfig.User, "password %q", password)
		require.Equal(t, "db.internal", poolCfg.ConnConfig.Host, "password %q", password)
		require.Equal(t, uint16(6543), poolCfg.ConnConfig.Port, "password %q", password)
		require.Equal(t, "shop", poolCfg.ConnConfig.Database, "password %q", password)
	}
}
