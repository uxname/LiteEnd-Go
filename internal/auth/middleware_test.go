package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	return NewMiddleware(nil, p, true)
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
	m := NewMiddleware(nil, fakeProfiles{}, false)
	require.Nil(t, m.AuthenticateCreds(context.Background(), "", ""))
}

// failingMockProfiles makes the mock-identity lookup fail, which must degrade to
// an anonymous request rather than a panic or a fabricated user.
type failingMockProfiles struct{ fakeProfiles }

func (failingMockProfiles) FindOrCreateMockUser(context.Context) (sqlc.Profile, error) {
	return sqlc.Profile{}, errors.New("db down")
}

func TestAuthenticateCreds_MockUserLookupFailureIsAnonymous(t *testing.T) {
	t.Parallel()
	m := newMockMiddleware(failingMockProfiles{})
	require.Nil(t, m.AuthenticateCreds(context.Background(), "", ""))
}

// --- RequireAuth (REST guard, e.g. POST /upload) ---

func TestRequireAuth_RejectsAnonymousWith401(t *testing.T) {
	t.Parallel()
	m := NewMiddleware(nil, fakeProfiles{}, false)
	h := m.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("the guarded handler must not run for an anonymous request")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/upload", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireAuth_PassesAuthenticatedUserDownstream(t *testing.T) {
	t.Parallel()
	m := newMockMiddleware(fakeProfiles{mockUser: sqlc.Profile{ID: 42, OidcSub: "mock-oidc-sub"}})
	var seen *sqlc.Profile
	h := m.RequireAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = UserFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/upload", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, seen, "the guarded handler must see the user in its context")
	require.Equal(t, int32(42), seen.ID)
}

// --- isProviderUnavailable: decides 503 (retry) vs 401 (re-authenticate) ---

func TestIsProviderUnavailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, true},
		{"wrapped net error", fmt.Errorf("jwks: %w", &net.DNSError{IsTimeout: true}), true},
		{"wrapped url error", fmt.Errorf("fetch: %w", &url.Error{Op: "Get", Err: errors.New("boom")}), true},
		{"invalid token", errors.New("token signature is invalid"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, c.want, isProviderUnavailable(c.err))
		})
	}
}
