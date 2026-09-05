package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	rootdb "github.com/uxname/liteend-go/db"
	"github.com/uxname/liteend-go/internal/config"
)

// lockRetryPeriodSeconds is how often a replica re-probes the advisory lock while
// another replica migrates. goose's default is 5s; 1s is the library minimum and
// keeps a cold start of the second replica short. The failure threshold below
// keeps the total wait at goose's default 5 minutes.
const (
	lockRetryPeriodSeconds    = 1
	lockRetryFailureThreshold = 300
)

// Migrate applies all pending goose migrations using the embedded migration FS.
// It retries on transient connection failures (the DB may still be starting).
//
// Migrations run under a Postgres session-level advisory lock, so several
// replicas starting at once on an empty database do not race: exactly one
// applies the migrations while the others wait and then find nothing to do.
func Migrate(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	migrations, err := fs.Sub(rootdb.Migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	sqlDB, err := sql.Open("pgx", cfg.DatabaseURL())
	if err != nil {
		return fmt.Errorf("open db for migrations: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	var lastErr error
	delay := config.DBRetryBaseDelay
	for attempt := 1; attempt <= config.DBMaxRetries; attempt++ {
		lastErr = sqlDB.PingContext(ctx)
		if lastErr == nil {
			break
		}
		// Full jitter: sleep a random duration in [0, delay] so concurrent
		// instances/replicas don't reconnect in lock-step (thundering herd).
		wait := time.Duration(rand.Int64N(int64(delay)) + 1) //nolint:gosec // jitter for retry backoff, not security-sensitive
		log.Warn("waiting for database before migrating",
			"attempt", attempt, "max", config.DBMaxRetries, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for database: %w", ctx.Err())
		case <-time.After(wait):
		}
		if delay *= 2; delay > config.DBRetryMaxDelay {
			delay = config.DBRetryMaxDelay
		}
	}
	if lastErr != nil {
		return fmt.Errorf("database not reachable for migrations: %w", lastErr)
	}

	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockTimeout(lockRetryPeriodSeconds, lockRetryFailureThreshold),
	)
	if err != nil {
		return fmt.Errorf("build migration locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations,
		goose.WithLogger(gooseLogger{log}),
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("build migration provider: %w", err)
	}

	applied, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	log.Info("migrations applied", "count", len(applied))
	return nil
}

// gooseLogger adapts slog to goose's logger interface.
type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Printf(format string, v ...any) { g.log.Info(fmt.Sprintf(format, v...)) }
func (g gooseLogger) Fatalf(format string, v ...any) { g.log.Error(fmt.Sprintf(format, v...)) }

// ensure stdlib driver is registered for database/sql.
var _ = stdlib.GetDefaultDriver
