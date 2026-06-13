// Command dbrestore restores a PostgreSQL backup file produced by dbbackup.
// Usage: dbrestore <backup-file-name>
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/uxname/liteend-go/internal/backup"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dbrestore <backup-file-name>")
		os.Exit(2)
	}

	cfg, err := config.LoadBackup()
	if err != nil {
		slog.Error("config load failed", "error", err)
		os.Exit(1)
	}
	log := logger.New(cfg.LogLevel)

	tool := backup.New(cfg, log)
	if err := tool.Restore(context.Background(), os.Args[1]); err != nil {
		log.Error("restore failed", "error", err)
		os.Exit(1)
	}
}
