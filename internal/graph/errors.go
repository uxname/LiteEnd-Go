package graph

import (
	"context"
	"errors"
	"log/slog"

	"github.com/99designs/gqlgen/graphql"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
)

// genericInternalMessage is returned to clients in production in place of raw
// internal error text, which may leak query/connection/schema details.
const genericInternalMessage = "Internal server error"

const codeInternal = "INTERNAL_SERVER_ERROR"

// newErrorPresenter builds the gqlgen error presenter. It maps domain errors to
// a stable code + request id (mirrors the TS error-formatter). In production it
// masks the message of internal (non-auth, non-validation) errors so raw details
// never reach the client; the original message is logged server-side instead.
func newErrorPresenter(isProd bool) graphql.ErrorPresenterFunc {
	return func(ctx context.Context, e error) *gqlerror.Error {
		gqlErr := graphql.DefaultErrorPresenter(ctx, e)
		if gqlErr.Extensions == nil {
			gqlErr.Extensions = map[string]any{}
		}

		requestID := middleware.GetReqID(ctx)
		if requestID == "" {
			requestID = config.FallbackRequestID
		}
		gqlErr.Extensions["requestId"] = requestID

		switch {
		case errors.Is(e, auth.ErrUnauthenticated):
			gqlErr.Extensions["code"] = "UNAUTHENTICATED"
			gqlErr.Extensions["statusCode"] = 401
		case errors.Is(e, auth.ErrForbidden):
			gqlErr.Extensions["code"] = "FORBIDDEN"
			gqlErr.Extensions["statusCode"] = 403
		default:
			if _, ok := gqlErr.Extensions["code"]; !ok {
				gqlErr.Extensions["code"] = codeInternal
				gqlErr.Extensions["statusCode"] = 500
			}
		}

		isInternal := gqlErr.Extensions["code"] == codeInternal

		// Every GraphQL error gets exactly one server-side line, with its ORIGINAL
		// message — before the production masking below throws it away. Client
		// faults (UNAUTHENTICATED, FORBIDDEN, BAD_USER_INPUT, …) log at Warn, ours
		// at Error, mirroring middleware.statusLevel. Without this a wave of 403s
		// or a broken query left no trace at all: GraphQL always answers HTTP 200,
		// so the access log cannot see it either.
		level := slog.LevelWarn
		if isInternal {
			level = slog.LevelError
		}
		logger.From(ctx).LogAttrs(ctx, level, "graphql_error",
			slog.String("error", gqlErr.Message),
			slog.Any("code", gqlErr.Extensions["code"]),
			slog.String("path", gqlErr.Path.String()),
			slog.String("request_id", requestID))

		// Mask only genuinely-internal errors in production; client-facing codes
		// (UNAUTHENTICATED, FORBIDDEN, BAD_USER_INPUT, …) carry safe messages.
		if isProd && isInternal {
			gqlErr.Message = genericInternalMessage
		}
		return gqlErr
	}
}
