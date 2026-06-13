package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/auth"
)

func TestErrorPresenter_Unauthenticated(t *testing.T) {
	t.Parallel()
	gqlErr := errorPresenter(context.Background(), auth.ErrUnauthenticated)
	require.Equal(t, "UNAUTHENTICATED", gqlErr.Extensions["code"])
	require.Equal(t, 401, gqlErr.Extensions["statusCode"])
	require.NotEmpty(t, gqlErr.Extensions["requestId"])
}

func TestErrorPresenter_Forbidden(t *testing.T) {
	t.Parallel()
	gqlErr := errorPresenter(context.Background(), auth.ErrForbidden)
	require.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
	require.Equal(t, 403, gqlErr.Extensions["statusCode"])
}

func TestErrorPresenter_GenericIsInternal(t *testing.T) {
	t.Parallel()
	gqlErr := errorPresenter(context.Background(), errors.New("boom"))
	require.Equal(t, "INTERNAL_SERVER_ERROR", gqlErr.Extensions["code"])
	require.Equal(t, 500, gqlErr.Extensions["statusCode"])
}
