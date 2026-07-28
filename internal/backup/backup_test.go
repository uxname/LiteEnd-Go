package backup

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/config"
)

func newTool(t *testing.T, cfg *config.BackupConfig) *Tool {
	t.Helper()
	return New(cfg, slog.New(slog.DiscardHandler))
}

// The extension is not cosmetic: Restore picks psql vs pg_restore from it, so a
// custom-format (binary) dump must never be named .sql.
func TestExt_MatchesTheToolThatCanRestoreIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		format   string
		compress bool
		want     string
	}{
		{"plain", true, "sql.gz"},
		{"plain", false, "sql"},
		// pg_dump's custom format compresses itself, so no external gzip pass —
		// the extension must not depend on BACKUP_COMPRESS.
		{"custom", true, "dump"},
		{"custom", false, "dump"},
	}
	for _, c := range cases {
		t.Run(c.format+"/compress="+map[bool]string{true: "on", false: "off"}[c.compress], func(t *testing.T) {
			t.Parallel()
			tool := newTool(t, &config.BackupConfig{
				BackupFormat:             c.format,
				BackupCompressionEnabled: c.compress,
			})
			require.Equal(t, c.want, tool.ext())
		})
	}
}

// writeDump creates a dump file with a known mtime so rotation order is defined.
func writeDump(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("-- dump\n"), 0o600))
	mtime := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	return path
}

func TestRotate_KeepsTheNewestNAndDeletesTheRest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldest := writeDump(t, dir, "db_2026-01-01T00-00-00Z.sql", 72*time.Hour)
	middle := writeDump(t, dir, "db_2026-01-02T00-00-00Z.sql", 48*time.Hour)
	newest := writeDump(t, dir, "db_2026-01-03T00-00-00Z.sql", 24*time.Hour)

	tool := newTool(t, &config.BackupConfig{
		BackupDir:      dir,
		BackupFormat:   "plain",
		BackupRotation: 2,
	})
	require.NoError(t, tool.rotate())

	require.NoFileExists(t, oldest, "the oldest dump beyond the rotation window must go")
	require.FileExists(t, middle)
	require.FileExists(t, newest)
}

func TestRotate_KeepsEverythingWhenUnderTheLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	only := writeDump(t, dir, "db_2026-01-01T00-00-00Z.sql", time.Hour)

	tool := newTool(t, &config.BackupConfig{
		BackupDir:      dir,
		BackupFormat:   "plain",
		BackupRotation: 5,
	})
	require.NoError(t, tool.rotate())
	require.FileExists(t, only, "a single dump under the rotation limit must survive")
}

// Rotation must not reach across formats: it prunes only the generation it
// currently produces, so switching BACKUP_FORMAT can never wipe the dumps that
// are still the only restorable ones.
func TestRotate_IgnoresOtherExtensions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	plain := writeDump(t, dir, "db_2026-01-01T00-00-00Z.sql", 72*time.Hour)
	gz := writeDump(t, dir, "db_2026-01-02T00-00-00Z.sql.gz", 48*time.Hour)
	custom := writeDump(t, dir, "db_2026-01-03T00-00-00Z.dump", 24*time.Hour)

	tool := newTool(t, &config.BackupConfig{
		BackupDir:      dir,
		BackupFormat:   "custom",
		BackupRotation: 1,
	})
	require.NoError(t, tool.rotate())

	require.FileExists(t, plain, "a .sql dump is not the custom generation")
	require.FileExists(t, gz, "a .sql.gz dump is not the custom generation")
	require.FileExists(t, custom, "the only .dump is within the rotation window")
}

func TestRotate_MissingDirIsAnError(t *testing.T) {
	t.Parallel()
	tool := newTool(t, &config.BackupConfig{
		BackupDir:      filepath.Join(t.TempDir(), "does-not-exist"),
		BackupFormat:   "plain",
		BackupRotation: 1,
	})
	require.Error(t, tool.rotate())
}

func TestRestore_MissingFileIsAnError(t *testing.T) {
	t.Parallel()
	tool := newTool(t, &config.BackupConfig{BackupDir: t.TempDir(), BackupFormat: "plain"})
	err := tool.Restore(t.Context(), "nope.sql")
	require.ErrorContains(t, err, "backup file not found")
}
