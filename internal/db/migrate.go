package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	rootdb "github.com/uxname/liteend-go/db"
	"github.com/uxname/liteend-go/internal/config"
)

// Migrate applies all pending goose migrations using the embedded migration FS.
// It retries on transient connection failures (the DB may still be starting).
func Migrate(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	goose.SetBaseFS(rootdb.Migrations)
	goose.SetLogger(gooseLogger{log})
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose set dialect: %w", err)
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
		log.Warn("waiting for database before migrating",
			"attempt", attempt, "max", config.DBMaxRetries, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > config.DBRetryMaxDelay {
			delay = config.DBRetryMaxDelay
		}
	}
	if lastErr != nil {
		return fmt.Errorf("database not reachable for migrations: %w", lastErr)
	}

	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	log.Info("migrations applied")
	return nil
}

// gooseLogger adapts slog to goose's logger interface.
type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Printf(format string, v ...any) { g.log.Info(fmt.Sprintf(format, v...)) }
func (g gooseLogger) Fatalf(format string, v ...any) { g.log.Error(fmt.Sprintf(format, v...)) }

// ensure stdlib driver is registered for database/sql.
var _ = stdlib.GetDefaultDriver
