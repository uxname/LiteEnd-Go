package config

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// setRequiredEnv sets every variable Load() refuses to start without, so a test
// can then break exactly one of them and know that is the reason it failed.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_PASSWORD", "p")
	t.Setenv("OIDC_ISSUER", "https://issuer")
	t.Setenv("OIDC_AUDIENCE", "aud")
	t.Setenv("OIDC_JWKS_URI", "https://issuer/jwks")
	t.Setenv("S3_ENDPOINT", "http://garage:3900")
	t.Setenv("S3_ACCESS_KEY_ID", "key")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("S3_PUBLIC_BASE_URL", "http://localhost:8080/uploads")
}

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
	setRequiredEnv(t)
	t.Setenv("OIDC_MOCK_ENABLED", "true")
	t.Setenv("NODE_ENV", "production")
	t.Setenv("CORS_ORIGIN", "http://localhost:3000")

	_, err := Load()
	require.Error(t, err, "mock auth must be rejected in production")
	require.Contains(t, err.Error(), "OIDC_MOCK_ENABLED")
}

// NODE_ENV is an exact allowlist: a near-miss like "Production" used to boot
// as a non-production app — mock auth allowed, introspection on, errors
// unmasked — while looking like a production deploy.
func TestLoad_NodeEnvMustBeKnown(t *testing.T) {
	for _, v := range []string{"Production", "PRODUCTION", " production", "prod", "staging"} {
		t.Run(v, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("NODE_ENV", v)
			t.Setenv("CORS_ORIGIN", "http://localhost:3000")

			_, err := Load()
			require.ErrorContains(t, err, "NODE_ENV")
		})
	}
}

// Mock auth hands anonymous callers ADMIN, so it is allowed only where the
// operator said "this is a development or test box" — never by omission.
func TestLoad_MockAllowedOnlyInDevelopmentAndTest(t *testing.T) {
	for env, allowed := range map[string]bool{"development": true, "test": true, "production": false} {
		t.Run(env, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("NODE_ENV", env)
			t.Setenv("OIDC_MOCK_ENABLED", "true")
			t.Setenv("CORS_ORIGIN", "http://localhost:3000")
			t.Setenv("ADMIN_PASSWORD", "not-the-default")

			_, err := Load()
			if allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "OIDC_MOCK_ENABLED")
		})
	}
}

func TestLoad_TrimsCORSOriginList(t *testing.T) {
	setRequiredEnv(t)
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

// C5: uploads live in S3-compatible storage, so the app must not boot half
// configured — a missing or blank storage variable has to stop it by name. The
// alternative is a replica that starts fine and only fails on the first upload,
// which is exactly the state this task exists to remove.
func TestLoad_RequiresS3Vars(t *testing.T) {
	for _, name := range []string{
		"S3_ENDPOINT",
		"S3_ACCESS_KEY_ID",
		"S3_SECRET_ACCESS_KEY",
		"S3_PUBLIC_BASE_URL",
	} {
		t.Run(name+"_unset", func(t *testing.T) {
			setRequiredEnv(t)
			// t.Setenv above recorded the previous value, so unsetting here is
			// undone after the test; plain non-setting would break on a machine
			// that exports the variable.
			require.NoError(t, os.Unsetenv(name))

			_, err := Load()
			require.Error(t, err, "%s is required", name)
			require.Contains(t, err.Error(), name)
		})

		t.Run(name+"_blank", func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(name, "")

			_, err := Load()
			require.Error(t, err, "%s must not be blank", name)
			require.Contains(t, err.Error(), name)
		})
	}
}

// C5: optional storage settings must not become accidentally required.
func TestLoad_S3Defaults(t *testing.T) {
	setRequiredEnv(t)
	// t.Setenv records whatever this machine had and restores it afterwards, so
	// the os.Unsetenv below is safe. Together they give the genuinely-unset state
	// a default is about — a developer exporting S3_BUCKET must not hide it.
	t.Setenv("S3_BUCKET", "")
	t.Setenv("S3_USE_SSL", "")
	require.NoError(t, os.Unsetenv("S3_BUCKET"))
	require.NoError(t, os.Unsetenv("S3_USE_SSL"))

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "uploads", cfg.S3Bucket)
	require.False(t, cfg.S3UseSSL)
}

// C5: a file URL is S3PublicBaseURL + "/" + key, so a trailing slash on the
// configured prefix would yield "//key".
func TestLoad_TrimsS3PublicBaseURLSlash(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("S3_PUBLIC_BASE_URL", "http://localhost:8080/uploads/")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "http://localhost:8080/uploads", cfg.S3PublicBaseURL)
}

// C11: pool size comes from the environment, and C8: the number of trusted
// proxy hops does too. Both have to keep working with nothing set — every
// existing deployment relies on the old hardcoded 10 and on a single proxy.
// LOG_LEVEL is parsed by slog itself. A value slog does not know stops the boot
// with the offending string in the message, instead of quietly running at info —
// which is how a "debug" session ends up logging nothing at all.
func TestLoad_LogLevel(t *testing.T) {
	setRequiredEnv(t)

	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"WARN":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo, // a blank line in a copied .env falls back to the default
	} {
		t.Setenv("LOG_LEVEL", in)
		cfg, err := Load()
		require.NoError(t, err, "LOG_LEVEL=%q", in)
		require.Equal(t, want, cfg.LogLevel, "LOG_LEVEL=%q", in)
	}

	t.Setenv("LOG_LEVEL", "verbose")
	_, err := Load()
	require.ErrorContains(t, err, "LogLevel")
	require.ErrorContains(t, err, "verbose")
}

func TestLoad_PoolAndProxyDefaults(t *testing.T) {
	setRequiredEnv(t)
	// Same restore-then-unset trick as above: assert the default, not the value
	// that happens to be exported on the machine running the test.
	t.Setenv("DB_POOL_MAX", "")
	t.Setenv("TRUSTED_PROXY_HOPS", "")
	require.NoError(t, os.Unsetenv("DB_POOL_MAX"))
	require.NoError(t, os.Unsetenv("TRUSTED_PROXY_HOPS"))

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, int32(10), cfg.DBPoolMax, "DB_POOL_MAX default")
	require.Equal(t, 1, cfg.TrustedProxyHops, "TRUSTED_PROXY_HOPS default")
}

// C11/C8: and the environment actually overrides them.
func TestLoad_PoolAndProxyOverridden(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DB_POOL_MAX", "4")
	t.Setenv("TRUSTED_PROXY_HOPS", "2")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, int32(4), cfg.DBPoolMax)
	require.Equal(t, 2, cfg.TrustedProxyHops)
}

// --- file links -------------------------------------------------------------

//nolint:paralleltest // setRequiredEnv calls t.Setenv, which rules out t.Parallel
func TestLoad_FilesArePrivateByDefault(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, FileVisibilityPrivate, cfg.FileVisibility, "the safe mode is the default one")
	require.False(t, cfg.FilesArePublic())
	require.Equal(t, time.Hour, cfg.FileLinkTTL(),
		"the default link lifetime has to outlive the page that shows the file")
}

func TestLoad_RejectsUnknownFileVisibility(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("FILE_VISIBILITY", "semi-public")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "FILE_VISIBILITY")
}

// A signed link is <public endpoint>/<bucket>/<key> and the signature covers
// that path, so a public base that does not end in the bucket produces links
// the storage refuses — caught at boot, not at the first upload.
func TestLoad_PrivateModeRequiresBucketInPublicBaseURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("S3_PUBLIC_BASE_URL", "http://localhost:3902")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "S3_PUBLIC_BASE_URL")

	// A prefix that merely ENDS in the bucket is not enough either: the signing
	// client keeps only the host, so the extra "/files" would never appear in a
	// signed link and every avatar would 404 at an address nobody can debug.
	t.Setenv("S3_PUBLIC_BASE_URL", "http://localhost:3902/files/uploads")
	_, err = Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "S3_PUBLIC_BASE_URL")

	// Public mode serves that same address straight from the storage's web
	// endpoint, where the bucket comes from the host name instead.
	t.Setenv("S3_PUBLIC_BASE_URL", "http://localhost:3902")
	t.Setenv("FILE_VISIBILITY", FileVisibilityPublic)
	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.FilesArePublic())
}

// The scheme is not decoration: the signing client reads TLS off it alone, so a
// typo signs http links against a host that only listens on 443 — and url.Parse
// is happy with both of these. A query or fragment would sit between the bucket
// and the key.
func TestLoad_RejectsAPublicBaseThatIsNotAPlainURL(t *testing.T) {
	for _, base := range []string{
		"htps://files.example.test/uploads", // scheme typo
		"//files.example.test/uploads",      // protocol-relative paste
		"files.example.test/uploads",        // no scheme at all
		"http://files.example.test/uploads?versionId=1",
		"http://files.example.test/uploads#frag",
	} {
		t.Run(base, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("S3_PUBLIC_BASE_URL", base)

			_, err := Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), "S3_PUBLIC_BASE_URL")
		})
	}
}

func TestLoad_RejectsImpossibleLinkLifetimes(t *testing.T) {
	for _, ttl := range []string{"0", "-5", "20160"} { // 20160 minutes = 14 days
		t.Run(ttl, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("FILE_LINK_TTL_MINUTES", ttl)

			_, err := Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), "FILE_LINK_TTL_MINUTES")
		})
	}
}
