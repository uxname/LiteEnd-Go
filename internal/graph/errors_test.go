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
	gqlErr := newErrorPresenter(false)(context.Background(), auth.ErrUnauthenticated)
	require.Equal(t, "UNAUTHENTICATED", gqlErr.Extensions["code"])
	require.Equal(t, 401, gqlErr.Extensions["statusCode"])
	require.NotEmpty(t, gqlErr.Extensions["requestId"])
}

func TestErrorPresenter_Forbidden(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(false)(context.Background(), auth.ErrForbidden)
	require.Equal(t, "FORBIDDEN", gqlErr.Extensions["code"])
	require.Equal(t, 403, gqlErr.Extensions["statusCode"])
}

func TestErrorPresenter_GenericIsInternal(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(false)(context.Background(), errors.New("boom"))
	require.Equal(t, "INTERNAL_SERVER_ERROR", gqlErr.Extensions["code"])
	require.Equal(t, 500, gqlErr.Extensions["statusCode"])
	require.Equal(t, "boom", gqlErr.Message, "dev mode keeps the original message")
}

func TestErrorPresenter_ProductionMasksInternal(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(true)(context.Background(), errors.New("connection string leak"))
	require.Equal(t, "INTERNAL_SERVER_ERROR", gqlErr.Extensions["code"])
	require.Equal(t, genericInternalMessage, gqlErr.Message, "prod masks internal error text")
}

func TestErrorPresenter_ProductionKeepsAuthMessage(t *testing.T) {
	t.Parallel()
	gqlErr := newErrorPresenter(true)(context.Background(), auth.ErrUnauthenticated)
	require.Equal(t, "UNAUTHENTICATED", gqlErr.Extensions["code"])
	require.NotEqual(t, genericInternalMessage, gqlErr.Message, "client-safe errors are not masked")
}
