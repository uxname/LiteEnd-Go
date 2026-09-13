// Package config loads and validates application configuration from the
// environment. It mirrors the variables documented in .env.example.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Config holds all runtime configuration, parsed from environment variables.
type Config struct {
	// Application
	Port       int      `env:"PORT" envDefault:"4000"`
	CORSOrigin []string `env:"CORS_ORIGIN" envSeparator:","`
	LogLevel   string   `env:"LOG_LEVEL" envDefault:"info"`
	Env        string   `env:"NODE_ENV" envDefault:"development"`
	// TrustedProxyHops is how many reverse proxies sit in front of this app. The
	// client address is taken that many entries from the RIGHT of X-Forwarded-For,
	// so a header forged by the client cannot impersonate another address.
	TrustedProxyHops int `env:"TRUSTED_PROXY_HOPS" envDefault:"1"`

	// Database
	DatabaseHost     string `env:"DATABASE_HOST" envDefault:"localhost"`
	DatabasePort     int    `env:"DATABASE_PORT" envDefault:"5432"`
	DatabaseUser     string `env:"DATABASE_USER" envDefault:"postgres"`
	DatabasePassword string `env:"DATABASE_PASSWORD,required"`
	DatabaseName     string `env:"DATABASE_NAME" envDefault:"postgres"`
	// DBPoolMax is the pool size of ONE replica. Sizing rule:
	// replicas x DB_POOL_MAX must stay below the Postgres max_connections limit.
	DBPoolMax int32 `env:"DB_POOL_MAX" envDefault:"10"`

	// Redis
	RedisHost     string `env:"REDIS_HOST" envDefault:"localhost"`
	RedisPort     int    `env:"REDIS_PORT" envDefault:"6379"`
	RedisPassword string `env:"REDIS_PASSWORD"`

	// S3-compatible object storage for uploads. Two addresses on purpose:
	// S3Endpoint is reachable from INSIDE the container network only (e.g.
	// http://garage:3900), S3PublicBaseURL is what the browser gets.
	// The credentials carry no default: an unset one must stop the boot, and an
	// empty one must too (a blank line in a copied .env is the usual way this
	// breaks), hence required+notEmpty rather than required alone.
	S3Endpoint        string `env:"S3_ENDPOINT,required,notEmpty"`
	S3AccessKeyID     string `env:"S3_ACCESS_KEY_ID,required,notEmpty"`
	S3SecretAccessKey string `env:"S3_SECRET_ACCESS_KEY,required,notEmpty"`
	S3Bucket          string `env:"S3_BUCKET" envDefault:"uploads"`
	S3UseSSL          bool   `env:"S3_USE_SSL" envDefault:"false"`
	// S3PublicBaseURL is the full public link prefix INCLUDING the bucket name, as
	// seen by the browser from outside (e.g. http://localhost:8080/uploads).
	// A file URL is S3PublicBaseURL + "/" + object key.
	S3PublicBaseURL string `env:"S3_PUBLIC_BASE_URL,required,notEmpty"`

	// FileVisibility decides how a stored file is handed to a browser.
	//
	//	private (default) — the bucket stays closed to anonymous readers and the
	//	                    API hands out a SIGNED link that expires after
	//	                    FileLinkTTLMinutes. A plain request for the same file
	//	                    is refused by the storage.
	//	public            — the bucket is world-readable and the API hands out
	//	                    the permanent link. Simpler, cacheable, and anyone
	//	                    holding the URL keeps access forever.
	//
	// It is a deployment decision, not a per-file one: see docs/adr/0003.
	FileVisibility string `env:"FILE_VISIBILITY" envDefault:"private"`
	// FileLinkTTLMinutes is how long a signed link stays valid. Only read in
	// private mode. The link is a bearer token for that one object, so shorter is
	// safer — but a link that dies while the page holding it is still on screen
	// is a broken image, and the default is picked for that side of the trade:
	// an hour outlives a cached page, a lazily-scrolled list and a coffee break.
	// Lower it when the files are worth more than the convenience.
	FileLinkTTLMinutes int `env:"FILE_LINK_TTL_MINUTES" envDefault:"60"`

	// OIDC
	OIDCIssuer      string `env:"OIDC_ISSUER,required"`
	OIDCAudience    string `env:"OIDC_AUDIENCE,required"`
	OIDCJWKSURI     string `env:"OIDC_JWKS_URI,required"`
	OIDCMockEnabled bool   `env:"OIDC_MOCK_ENABLED" envDefault:"false"`

	// Host ports of the companion dev UIs (used to render links on /dev).
	DBStudioPort    int `env:"DB_STUDIO_PORT" envDefault:"5100"`
	RedisStudioPort int `env:"REDIS_STUDIO_PORT" envDefault:"5200"`
	AsynqmonPort    int `env:"ASYNQMON_PORT" envDefault:"5300"`

	// Basic-auth credentials guarding the app's dev pages (/dev, /playground,
	// /swagger). The external dashboards use the same creds via the auth proxy.
	AdminUser     string `env:"ADMIN_USER" envDefault:"admin"`
	AdminPassword string `env:"ADMIN_PASSWORD" envDefault:"admin"`
}

// IsProduction reports whether the app runs in production mode.
func (c *Config) IsProduction() bool { return c.Env == "production" }

// FileLinkTTL is how long a signed file link stays valid.
func (c *Config) FileLinkTTL() time.Duration {
	return time.Duration(c.FileLinkTTLMinutes) * time.Minute
}

// FilesArePublic reports whether stored files are served by a permanent public
// link instead of a signed one.
func (c *Config) FilesArePublic() bool { return c.FileVisibility == FileVisibilityPublic }

// validateFileLinks refuses a file configuration that would only fail later, at
// the first upload, as a link nobody can open.
func (c *Config) validateFileLinks() error {
	if c.FileVisibility != FileVisibilityPrivate && c.FileVisibility != FileVisibilityPublic {
		return fmt.Errorf("FILE_VISIBILITY must be %q or %q, got %q",
			FileVisibilityPrivate, FileVisibilityPublic, c.FileVisibility)
	}
	if c.FilesArePublic() {
		return nil
	}
	if c.FileLinkTTLMinutes < 1 || c.FileLinkTTL() > MaxFileLinkTTL {
		return fmt.Errorf("FILE_LINK_TTL_MINUTES must be between 1 and %d (the S3 signature limit), got %d",
			int(MaxFileLinkTTL.Minutes()), c.FileLinkTTLMinutes)
	}
	// A signed link is built by the S3 client itself as <host>/<bucket>/<key>,
	// and the signature covers that host and path — so the public address the
	// browser uses has to BE <host>/<bucket>, exactly. The signing client keeps
	// only the HOST of this value (minio-go takes a host, not a URL), so a deeper
	// prefix such as https://example.com/files/uploads is silently dropped: links
	// would be signed for /uploads/<key> and the browser would ask for a path
	// that does not exist. Ending in the bucket name is therefore not enough —
	// the path has to be nothing but the bucket.
	u, err := url.Parse(c.S3PublicBaseURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf(
			"S3_PUBLIC_BASE_URL must be an absolute http(s) URL when FILE_VISIBILITY=private, got %q",
			c.S3PublicBaseURL)
	}
	if u.Path != "/"+c.S3Bucket {
		return fmt.Errorf(
			"S3_PUBLIC_BASE_URL must be exactly <public S3 API address>/%s when FILE_VISIBILITY=private "+
				"(signed links are <public endpoint>/<bucket>/<key>, and only the host of this value is "+
				"used to sign them), got %q", c.S3Bucket, c.S3PublicBaseURL)
	}
	return nil
}

// DatabaseURL builds a libpq-style connection string for pgx.
func (c *Config) DatabaseURL() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=disable",
		c.DatabaseUser, c.DatabasePassword, c.DatabaseHost, c.DatabasePort, c.DatabaseName,
	)
}

// RedisAddr returns the host:port for the Redis client.
func (c *Config) RedisAddr() string {
	return fmt.Sprintf("%s:%d", c.RedisHost, c.RedisPort)
}

// trimmedNonEmpty trims each entry of a comma-separated env list and drops the
// empties (a trailing comma or a blank value).
func trimmedNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Load reads .env (if present) and parses environment variables into Config.
// A missing .env file is not an error — variables may come from the real
// environment (e.g. inside a container).
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.OIDCMockEnabled && cfg.IsProduction() {
		return nil, errors.New("OIDC_MOCK_ENABLED must not be true in production")
	}

	// `env` splits on "," without trimming, so the natural
	// `CORS_ORIGIN=http://a, http://b` yields " http://b" — an origin that matches
	// nothing. This list is now also the WebSocket handshake allowlist, so a stray
	// space silently kills subscriptions from that origin, not just CORS.
	cfg.CORSOrigin = trimmedNonEmpty(cfg.CORSOrigin)

	// File URLs are built as S3PublicBaseURL + "/" + key, so a trailing slash on
	// the configured value would produce "//key" — a link that 404s on some
	// gateways and defeats caching on the rest.
	cfg.S3PublicBaseURL = strings.TrimRight(cfg.S3PublicBaseURL, "/")

	if err := cfg.validateFileLinks(); err != nil {
		return nil, err
	}

	// An empty CORS_ORIGIN makes go-chi/cors allow every origin; combined with
	// AllowCredentials that is unsafe in production, so require an explicit
	// allowlist there (fail-fast). In development an empty value is tolerated.
	if cfg.IsProduction() && len(cfg.CORSOrigin) == 0 {
		return nil, errors.New("CORS_ORIGIN must be set to an explicit origin allowlist in production")
	}

	return cfg, nil
}
