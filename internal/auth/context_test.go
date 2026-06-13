package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/db/sqlc"
)

func TestRequire_NoUser(t *testing.T) {
	t.Parallel()
	_, err := Require(context.Background())
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestRequire_WithUser(t *testing.T) {
	t.Parallel()
	user := &sqlc.Profile{ID: 1, OidcSub: "s"}
	ctx := WithUser(context.Background(), user)
	got, err := Require(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(1), got.ID)
}

func TestRequireRole(t *testing.T) {
	t.Parallel()
	admin := &sqlc.Profile{ID: 1, Roles: []sqlc.ProfileRole{sqlc.ProfileRoleUSER, sqlc.ProfileRoleADMIN}}
	user := &sqlc.Profile{ID: 2, Roles: []sqlc.ProfileRole{sqlc.ProfileRoleUSER}}

	_, err := RequireRole(WithUser(context.Background(), admin), sqlc.ProfileRoleADMIN)
	require.NoError(t, err)

	_, err = RequireRole(WithUser(context.Background(), user), sqlc.ProfileRoleADMIN)
	require.ErrorIs(t, err, ErrForbidden)

	_, err = RequireRole(context.Background(), sqlc.ProfileRoleADMIN)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestStripBearer(t *testing.T) {
	t.Parallel()
	require.Equal(t, "abc", StripBearer("Bearer abc"))
	require.Equal(t, "abc", StripBearer("bearer abc"))
	require.Equal(t, "raw", StripBearer("raw"))
}
