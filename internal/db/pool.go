// Package db owns the PostgreSQL connection pool, migrations, and sqlc queries.
package db

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// DB bundles the pgx pool with the generated sqlc queries.
type DB struct {
	Pool    *pgxpool.Pool
	Queries *sqlc.Queries
}

// New opens a pgx pool, registers custom enum types, and verifies connectivity.
// It takes no logger: the query tracer logs through logger.From(ctx) so a slow
// or failed query carries the request_id of whoever caused it.
func New(ctx context.Context, cfg *config.Config) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL())
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}

	poolCfg.MaxConns = config.DBPoolMax
	poolCfg.MaxConnIdleTime = config.DBIdleTimeout
	poolCfg.MaxConnLifetime = config.DBMaxConnLifetime
	poolCfg.HealthCheckPeriod = config.DBHealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = config.DBConnectTimeout

	// Cap any single statement server-side so a hung or runaway query cannot hold
	// a pooled connection forever and starve the pool (cascading-failure guard).
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(
		config.DBStatementTimeout.Milliseconds(), 10,
	)

	// Register the profile_role enum (and its array) on every new connection so
	// pgx can decode profile_role[] into []sqlc.ProfileRole.
	poolCfg.AfterConnect = registerEnumTypes

	// Slow/failed queries are the one DB signal worth having in the log; without
	// it a latency incident has no evidence beyond the request duration.
	poolCfg.ConnConfig.Tracer = slowQueryTracer{}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return &DB{Pool: pool, Queries: sqlc.New(pool)}, nil
}

// Close releases all pooled connections.
func (d *DB) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}

// Ping checks database connectivity (used by the health endpoint).
func (d *DB) Ping(ctx context.Context) error {
	if err := d.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}
