// Package config loads and validates application configuration from the
// environment. It mirrors the variables documented in .env.example.
package config

import (
	"fmt"
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

	// Database
	DatabaseHost     string `env:"DATABASE_HOST" envDefault:"localhost"`
	DatabasePort     int    `env:"DATABASE_PORT" envDefault:"5432"`
	DatabaseUser     string `env:"DATABASE_USER" envDefault:"postgres"`
	DatabasePassword string `env:"DATABASE_PASSWORD,required"`
	DatabaseName     string `env:"DATABASE_NAME" envDefault:"postgres"`

	// Redis
	RedisHost     string `env:"REDIS_HOST" envDefault:"localhost"`
	RedisPort     int    `env:"REDIS_PORT" envDefault:"6379"`
	RedisPassword string `env:"REDIS_PASSWORD"`

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
		return nil, fmt.Errorf("OIDC_MOCK_ENABLED must not be true in production")
	}

	return cfg, nil
}

// BackupConfig is the minimal configuration for the dbbackup/dbrestore tools.
// It deliberately does NOT require OIDC settings (the backup container has none).
type BackupConfig struct {
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`

	DatabaseHost     string `env:"DATABASE_HOST" envDefault:"localhost"`
	DatabasePort     int    `env:"DATABASE_PORT" envDefault:"5432"`
	DatabaseUser     string `env:"DATABASE_USER" envDefault:"postgres"`
	DatabasePassword string `env:"DATABASE_PASSWORD,required"`
	DatabaseName     string `env:"DATABASE_NAME" envDefault:"postgres"`

	BackupDir      string        `env:"BACKUP_DIR" envDefault:"./data/database_backups"`
	BackupInterval time.Duration `env:"BACKUP_INTERVAL" envDefault:"24h"`
	BackupRotation int           `env:"BACKUP_ROTATION" envDefault:"5"`
	BackupFormat   string        `env:"BACKUP_FORMAT" envDefault:"plain"`
	BackupCompress bool          `env:"BACKUP_COMPRESS" envDefault:"true"`
}

// LoadBackup loads configuration for the backup tools (no OIDC required).
func LoadBackup() (*BackupConfig, error) {
	_ = godotenv.Load()
	cfg := &BackupConfig{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse backup config: %w", err)
	}
	return cfg, nil
}
