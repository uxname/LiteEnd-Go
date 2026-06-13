package auth

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// fakeProfiles implements Profiles for the mock-auth path (no real OIDC).
type fakeProfiles struct {
	mockUser sqlc.Profile
	bySub    map[string]sqlc.Profile
}

func (f fakeProfiles) FindOrCreateBySub(_ context.Context, sub string) (sqlc.Profile, error) {
	return sqlc.Profile{OidcSub: sub}, nil
}

func (f fakeProfiles) FindBySub(_ context.Context, sub string) (*sqlc.Profile, error) {
	if p, ok := f.bySub[sub]; ok {
		return &p, nil
	}
	// Any non-nil error signals "not found" to AuthenticateCreds.
	return nil, context.Canceled
}

func (f fakeProfiles) FindOrCreateMockUser(context.Context) (sqlc.Profile, error) {
	return f.mockUser, nil
}

func newMockMiddleware(p Profiles) *Middleware {
	return NewMiddleware(nil, p, slog.New(slog.DiscardHandler), true)
}

func TestAuthenticateCreds_MockDefaultUser(t *testing.T) {
	t.Parallel()
	m := newMockMiddleware(fakeProfiles{mockUser: sqlc.Profile{ID: 42, OidcSub: "mock-oidc-sub"}})
	user := m.AuthenticateCreds(context.Background(), "", "")
	require.NotNil(t, user)
	require.Equal(t, int32(42), user.ID)
}

func TestAuthenticateCreds_MockSubImpersonation(t *testing.T) {
	t.Parallel()
	m := newMockMiddleware(fakeProfiles{
		mockUser: sqlc.Profile{ID: 42},
		bySub:    map[string]sqlc.Profile{"alice": {ID: 7, OidcSub: "alice"}},
	})
	user := m.AuthenticateCreds(context.Background(), "", "alice")
	require.NotNil(t, user)
	require.Equal(t, int32(7), user.ID, "x-mock-sub should impersonate the matching profile")
}

func TestAuthenticateCreds_NoMockNoBearerIsNil(t *testing.T) {
	t.Parallel()
	m := NewMiddleware(nil, fakeProfiles{}, slog.New(slog.DiscardHandler), false)
	require.Nil(t, m.AuthenticateCreds(context.Background(), "", ""))
}
