package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseURL(t *testing.T) {
	c := &Config{
		DatabaseUser: "u", DatabasePassword: "p",
		DatabaseHost: "h", DatabasePort: 5432, DatabaseName: "db",
	}
	require.Equal(t, "postgres://u:p@h:5432/db?sslmode=disable", c.DatabaseURL())
}

func TestRedisAddr(t *testing.T) {
	c := &Config{RedisHost: "redis", RedisPort: 6379}
	require.Equal(t, "redis:6379", c.RedisAddr())
}

func TestIsProduction(t *testing.T) {
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
