//go:build integration

// Migration tests need a real Postgres (advisory locks have no in-memory fake),
// so they live behind the `integration` tag like the rest of the container-backed
// suite. Run with: go test -tags=integration ./internal/db
package db_test

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db"
)

// replicas is the number of app copies migrating the same empty database at the
// same moment — the cold-start scenario C7 describes.
const replicas = 6

// TestC7_ConcurrentColdStartMigratesOnce covers C7: several replicas starting on
// an empty database must not race. Every replica's Migrate must succeed, and the
// migration must be recorded exactly once.
//
// Without goose's Postgres session locker this fails: the copies enter
// db/migrations/00001_init.sql concurrently and Postgres rejects the losers
// ("type profile_role already exists" / duplicate key on goose_db_version).
func TestC7_ConcurrentColdStartMigratesOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	pgC, err := tcpostgres.Run(ctx, "postgres:18.1-alpine",
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	host, err := pgC.Host(ctx)
	require.NoError(t, err)
	port, err := pgC.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	portNum, err := strconv.Atoi(port.Port())
	require.NoError(t, err)

	cfg := &config.Config{
		DatabaseHost:     host,
		DatabasePort:     portNum,
		DatabaseUser:     "postgres",
		DatabasePassword: "postgres",
		DatabaseName:     "postgres",
	}
	log := slog.New(slog.DiscardHandler)

	// Each replica gets its own Migrate call (its own *sql.DB and its own goose
	// provider) — sharing one provider would serialise them on goose's in-process
	// mutex and prove nothing about cross-process safety.
	start := make(chan struct{})
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	wg.Add(replicas)
	for i := range replicas {
		go func() {
			defer wg.Done()
			<-start
			errs[i] = db.Migrate(ctx, cfg, log)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "replica %d must wait for the lock, not fail", i)
	}

	sqlDB, err := sql.Open("pgx", cfg.DatabaseURL())
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	// Exactly one row per migration version: a second applier would insert a
	// duplicate even if its DDL somehow slipped through.
	var dupes int
	require.NoError(t, sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM (
			SELECT version_id FROM goose_db_version GROUP BY version_id HAVING count(*) > 1
		) d`).Scan(&dupes))
	require.Zero(t, dupes, "a migration version was applied more than once")

	// And the migrations really ran — otherwise the assertions above are vacuous.
	var profiles int
	require.NoError(t, sqlDB.QueryRowContext(ctx,
		`SELECT count(*) FROM profiles`).Scan(&profiles))
	require.Zero(t, profiles)
}
