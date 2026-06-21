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

// Redacted is the placeholder substituted for sensitive values in logs.
const Redacted = "[REDACTED]"

// SensitiveKey reports whether a key should have its value redacted (case-insensitive).
func SensitiveKey(key string) bool {
	_, ok := sensitiveKeys[strings.ToLower(key)]
	return ok
}

// RedactValue returns v with every value under a sensitive key replaced by
// Redacted, descending recursively into nested maps and slices. The input is not
// mutated. This is the single source of truth for structured redaction, shared
// by the slog handler and the GraphQL operation logger.
func RedactValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			if SensitiveKey(k) {
				out[k] = Redacted
				continue
			}
			out[k] = RedactValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, vv := range val {
			out[i] = RedactValue(vv)
		}
		return out
	default:
		return v
	}
}

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
	if SensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	// Descend into structured values (e.g. slog.Any of a map) so a secret nested
	// under {input:{token:...}} is redacted, not just top-level attribute keys.
	if a.Value.Kind() == slog.KindAny {
		if red := RedactValue(a.Value.Any()); red != nil {
			return slog.Any(a.Key, red)
		}
	}
	return a
}
