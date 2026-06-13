// Package logger builds the application's structured slog logger.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// sensitiveKeys are redacted from log attributes. Mirrors the SENSITIVE_KEYS
// list from the TypeScript gql-logging.interceptor.
var sensitiveKeys = map[string]struct{}{ //nolint:gochecknoglobals // static redaction allowlist
	"password":      {},
	"token":         {},
	"secret":        {},
	"authorization": {},
	"credentials":   {},
	"cookie":        {},
	"sig":           {},
}

const redacted = "[REDACTED]"

// New returns a JSON slog.Logger at the given level ("debug","info","warn","error").
// Attribute keys matching sensitiveKeys are redacted.
func New(level string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       parseLevel(level),
		ReplaceAttr: redactSensitive,
	})
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "trace":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func redactSensitive(_ []string, a slog.Attr) slog.Attr {
	if _, ok := sensitiveKeys[strings.ToLower(a.Key)]; ok {
		return slog.String(a.Key, redacted)
	}
	return a
}
