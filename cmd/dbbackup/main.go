// Command dbbackup runs scheduled PostgreSQL backups (pg_dump + rotation).
// Mirrors the TypeScript db-backup-tool.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/uxname/liteend-go/internal/backup"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
)

func main() {
	cfg, err := config.LoadBackup()
	if err != nil {
		slog.Error("config load failed", "error", err)
		os.Exit(1)
	}
	log := logger.New(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tool := backup.New(cfg, log)
	if err := tool.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("backup tool exited", "error", err)
		os.Exit(1)
	}
}
