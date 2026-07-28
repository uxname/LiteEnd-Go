package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseURL(t *testing.T) {
	t.Parallel()
	c := &Config{
		DatabaseUser: "u", DatabasePassword: "p",
		DatabaseHost: "h", DatabasePort: 5432, DatabaseName: "db",
	}
	require.Equal(t, "postgres://u:p@h:5432/db?sslmode=disable", c.DatabaseURL())
}

func TestRedisAddr(t *testing.T) {
	t.Parallel()
	c := &Config{RedisHost: "redis", RedisPort: 6379}
	require.Equal(t, "redis:6379", c.RedisAddr())
}

func TestIsProduction(t *testing.T) {
	t.Parallel()
	require.True(t, (&Config{Env: "production"}).IsProduction())
	require.False(t, (&Config{Env: "development"}).IsProduction())
}

func TestLoad_MockInProductionRejected(t *testing.T) {
	t.Setenv("DATABASE_PASSWORD", "p")
	t.Setenv("OIDC_ISSUER", "https://issuer")
	t.Setenv("OIDC_AUDIENCE", "aud")
	t.Setenv("OIDC_JWKS_URI", "https://issuer/jwks")
	t.Setenv("OIDC_MOCK_ENABLED", "true")
	t.Setenv("NODE_ENV", "production")

	_, err := Load()
	require.Error(t, err, "mock auth must be rejected in production")
}

func TestLoad_TrimsCORSOriginList(t *testing.T) {
	t.Setenv("DATABASE_PASSWORD", "p")
	t.Setenv("OIDC_ISSUER", "https://issuer")
	t.Setenv("OIDC_AUDIENCE", "aud")
	t.Setenv("OIDC_JWKS_URI", "https://issuer/jwks")
	// The natural way to write a list — with spaces after the commas, plus a
	// trailing comma. Untrimmed, " http://localhost:4000" matches no origin, and
	// since this list also authorizes WebSocket handshakes that silently kills
	// subscriptions from it.
	t.Setenv("CORS_ORIGIN", "http://localhost:3000, http://localhost:4000 ,")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(
		t,
		[]string{"http://localhost:3000", "http://localhost:4000"},
		cfg.CORSOrigin,
	)
}

func TestLoadBackup_RejectsDestructiveRotation(t *testing.T) {
	t.Setenv("DATABASE_PASSWORD", "p")
	// rotate() keeps the newest N files, so 0 deletes the dump it just created and
	// a negative value slices out of range and panics the backup daemon.
	for _, rotation := range []string{"0", "-1"} {
		t.Setenv("BACKUP_ROTATION", rotation)
		_, err := LoadBackup()
		require.Error(t, err, "BACKUP_ROTATION=%s must be rejected at startup", rotation)
	}
}

func TestLoadBackup_RejectsUnknownFormat(t *testing.T) {
	t.Setenv("DATABASE_PASSWORD", "p")
	t.Setenv("BACKUP_FORMAT", "tar")
	_, err := LoadBackup()
	require.Error(t, err, "only plain and custom are implemented by Restore")
}

func TestLoadBackup_AcceptsValidSettings(t *testing.T) {
	t.Setenv("DATABASE_PASSWORD", "p")
	// Set both explicitly rather than asserting the defaults: Load reads the
	// developer's .env, so a default-value assertion would depend on the machine.
	t.Setenv("BACKUP_ROTATION", "3")
	t.Setenv("BACKUP_FORMAT", "custom")
	cfg, err := LoadBackup()
	require.NoError(t, err)
	require.Equal(t, 3, cfg.BackupRotation)
	require.Equal(t, "custom", cfg.BackupFormat)
}
