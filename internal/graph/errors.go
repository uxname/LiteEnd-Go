package graph

import (
	"context"
	"errors"

	"github.com/99designs/gqlgen/graphql"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/config"
)

// errorPresenter maps domain errors to GraphQL errors with a stable code and a
// request id (mirrors the TS error-formatter / all-exceptions-filter).
func errorPresenter(ctx context.Context, e error) *gqlerror.Error {
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
			gqlErr.Extensions["code"] = "INTERNAL_SERVER_ERROR"
			gqlErr.Extensions["statusCode"] = 500
		}
	}
	return gqlErr
}
