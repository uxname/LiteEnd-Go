package graph

import (
	"context"
	"log/slog"
	"time"

	"github.com/99designs/gqlgen/graphql"

	"github.com/uxname/liteend-go/internal/logger"
)

// sensitiveKeys mirrors the TS gql-logging.interceptor redaction list.
var sensitiveKeys = map[string]struct{}{ //nolint:gochecknoglobals // static redaction allowlist
	"password": {}, "token": {}, "secret": {}, "authorization": {},
	"credentials": {}, "cookie": {}, "sig": {},
}

// LoggingExtension logs each GraphQL operation: type, name, redacted variables,
// and latency. Implements gqlgen's HandlerExtension/ResponseInterceptor. It logs
// via the request-scoped logger (logger.From(ctx)), so each line inherits the
// request_id and user_id added by the HTTP middleware.
type LoggingExtension struct{}

// ExtensionName identifies the extension to gqlgen.
func (LoggingExtension) ExtensionName() string { return "OperationLogging" }

// Validate is a no-op required by the HandlerExtension interface.
func (LoggingExtension) Validate(graphql.ExecutableSchema) error { return nil }

// InterceptResponse logs the operation once it has been resolved.
func (*LoggingExtension) InterceptResponse(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	start := time.Now()
	oc := graphql.GetOperationContext(ctx)
	resp := next(ctx)

	opName := oc.OperationName
	if opName == "" {
		opName = "anonymous"
	}
	var opType string
	if oc.Operation != nil {
		opType = string(oc.Operation.Operation)
	}

	logger.From(ctx).LogAttrs(
		ctx, slog.LevelInfo, "graphql_operation",
		slog.String("operation", opName),
		slog.String("type", opType),
		slog.Any("variables", redactVariables(oc.Variables)),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		slog.Int("errors", len(resp.Errors)),
	)
	return resp
}

func redactVariables(vars map[string]any) map[string]any {
	if vars == nil {
		return nil
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		if _, sensitive := sensitiveKeys[lower(k)]; sensitive {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = v
	}
	return out
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
