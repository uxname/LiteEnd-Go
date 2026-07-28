// Package backup performs PostgreSQL backups (pg_dump) with rotation and
// restores (pg_restore/psql), mirroring the TypeScript db-backup-tool.
package backup

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uxname/liteend-go/internal/config"
)

// Tool runs scheduled backups and restores.
type Tool struct {
	cfg *config.BackupConfig
	log *slog.Logger
	mu  sync.Mutex // prevents concurrent backups
}

// New builds a backup Tool.
func New(cfg *config.BackupConfig, log *slog.Logger) *Tool {
	return &Tool{cfg: cfg, log: log}
}

// custom reports whether dumps are written in pg_dump's binary custom format,
// which only pg_restore can read.
func (t *Tool) custom() bool { return t.cfg.BackupFormat == "custom" }

func (t *Tool) ext() string {
	// The custom format is already compressed by pg_dump itself, so it never gets
	// an external gzip pass — and it must not claim a .sql name, because Restore
	// picks its tool from the extension.
	if t.custom() {
		return "dump"
	}
	if t.cfg.BackupCompressionEnabled {
		return "sql.gz"
	}
	return "sql"
}

// Run performs an initial backup and then repeats every BackupInterval until
// ctx is cancelled.
func (t *Tool) Run(ctx context.Context) error {
	if err := t.Backup(ctx); err != nil {
		t.log.Error("initial backup failed", "error", err)
	}
	ticker := time.NewTicker(t.cfg.BackupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("backup loop stopped: %w", ctx.Err())
		case <-ticker.C:
			if err := t.Backup(ctx); err != nil {
				t.log.Error("scheduled backup failed", "error", err)
			}
		}
	}
}

// Backup creates a single dump and rotates old files.
func (t *Tool) Backup(ctx context.Context) error {
	if !t.mu.TryLock() {
		t.log.Warn("another backup is in progress, skipping")
		return nil
	}
	defer t.mu.Unlock()

	if err := os.MkdirAll(t.cfg.BackupDir, 0o750); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}

	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	file := filepath.Join(t.cfg.BackupDir, fmt.Sprintf("%s_%s.%s", t.cfg.DatabaseName, ts, t.ext()))

	format := "p"
	if t.custom() {
		format = "c"
	}

	args := []string{
		"-h", t.cfg.DatabaseHost,
		"-p", strconv.Itoa(t.cfg.DatabasePort),
		"-U", t.cfg.DatabaseUser,
		"-d", t.cfg.DatabaseName,
		"-F", format,
		"-b",
	}

	t.log.Info("starting backup", "file", file)
	if err := t.runDump(ctx, args, file); err != nil {
		_ = os.Remove(file)
		return err
	}
	t.log.Info("backup completed", "file", file)
	return t.rotate()
}

func (t *Tool) runDump(ctx context.Context, args []string, outFile string) error {
	out, err := os.Create(outFile) //nolint:gosec // path built from config under backup dir
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer func() { _ = out.Close() }()

	dump := exec.CommandContext(ctx, "pg_dump", args...) //nolint:gosec // args from config
	dump.Env = append(os.Environ(), "PGPASSWORD="+t.cfg.DatabasePassword)
	dump.Stderr = os.Stderr

	// The custom format carries its own compression; piping it through gzip would
	// only cost CPU and hide the format from Restore.
	if t.cfg.BackupCompressionEnabled && !t.custom() {
		gzip := exec.CommandContext(ctx, "gzip")
		gzip.Stdout = out
		gzip.Stderr = os.Stderr
		pipe, err := dump.StdoutPipe()
		if err != nil {
			return fmt.Errorf("pg_dump stdout pipe: %w", err)
		}
		gzip.Stdin = pipe
		if err := gzip.Start(); err != nil {
			return fmt.Errorf("start gzip: %w", err)
		}
		if err := dump.Run(); err != nil {
			return fmt.Errorf("pg_dump: %w", err)
		}
		if err := gzip.Wait(); err != nil {
			return fmt.Errorf("gzip: %w", err)
		}
		return nil
	}

	dump.Stdout = out
	if err := dump.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	return nil
}

// rotate keeps only the newest BackupRotation files.
func (t *Tool) rotate() error {
	entries, err := os.ReadDir(t.cfg.BackupDir)
	if err != nil {
		return fmt.Errorf("read backup dir: %w", err)
	}
	suffix := "." + t.ext()
	type fileInfo struct {
		path  string
		mtime time.Time
	}
	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{filepath.Join(t.cfg.BackupDir, e.Name()), info.ModTime()})
	}
	if len(files) <= t.cfg.BackupRotation {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files[:len(files)-t.cfg.BackupRotation] {
		if err := os.Remove(f.path); err != nil {
			t.log.Warn("failed to remove old backup", "file", f.path, "error", err)
			continue
		}
		t.log.Info("deleted old backup", "file", f.path)
	}
	return nil
}

// Restore restores a backup file (gzip-aware) into the database.
func (t *Tool) Restore(ctx context.Context, fileName string) error {
	path := filepath.Join(t.cfg.BackupDir, fileName)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("backup file not found: %w", err)
	}

	connArgs := []string{
		"-h", t.cfg.DatabaseHost,
		"-p", strconv.Itoa(t.cfg.DatabasePort),
		"-U", t.cfg.DatabaseUser,
		"-d", t.cfg.DatabaseName,
	}
	// Without ON_ERROR_STOP psql reports success after skipping every failed
	// statement, so a half-restored database looks like a completed restore.
	psqlArgs := append(append([]string{}, connArgs...), "-v", "ON_ERROR_STOP=1")

	t.log.Info("restoring backup", "file", path)

	// A binary custom-format dump is not SQL text: only pg_restore reads it.
	// --exit-on-error makes it fail loudly like psql above (its default is to
	// continue and exit 0 with warnings).
	if strings.HasSuffix(fileName, ".dump") {
		args := append(append([]string{}, connArgs...), "--exit-on-error", path)
		restore := exec.CommandContext(ctx, "pg_restore", args...) //nolint:gosec // args from config, path under the backup dir
		restore.Env = append(os.Environ(), "PGPASSWORD="+t.cfg.DatabasePassword)
		restore.Stderr = os.Stderr
		if err := restore.Run(); err != nil {
			return fmt.Errorf("pg_restore: %w", err)
		}
		t.log.Info("restore completed", "file", path)
		return nil
	}

	if strings.HasSuffix(fileName, ".gz") {
		gunzip := exec.CommandContext(ctx, "gunzip", "-c", path) //nolint:gosec // path is under the configured backup dir
		psql := exec.CommandContext(ctx, "psql", psqlArgs...)    //nolint:gosec // args from config
		psql.Env = append(os.Environ(), "PGPASSWORD="+t.cfg.DatabasePassword)
		psql.Stderr = os.Stderr
		pipe, err := gunzip.StdoutPipe()
		if err != nil {
			return fmt.Errorf("gunzip stdout pipe: %w", err)
		}
		psql.Stdin = pipe
		if err := psql.Start(); err != nil {
			return fmt.Errorf("start psql: %w", err)
		}
		if err := gunzip.Run(); err != nil {
			return fmt.Errorf("gunzip: %w", err)
		}
		if err := psql.Wait(); err != nil {
			return fmt.Errorf("psql restore: %w", err)
		}
		return nil
	}

	in, err := os.Open(path) //nolint:gosec // path is under the configured backup dir
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer func() { _ = in.Close() }()
	psql := exec.CommandContext(ctx, "psql", psqlArgs...) //nolint:gosec // args from config
	psql.Env = append(os.Environ(), "PGPASSWORD="+t.cfg.DatabasePassword)
	psql.Stdin = in
	psql.Stderr = os.Stderr
	if err := psql.Run(); err != nil {
		return fmt.Errorf("psql restore: %w", err)
	}
	t.log.Info("restore completed", "file", path)
	return nil
}
