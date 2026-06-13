package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// Profiles is the profile operations the auth layer depends on.
type Profiles interface {
	FindOrCreateBySub(ctx context.Context, sub string) (sqlc.Profile, error)
	FindBySub(ctx context.Context, sub string) (*sqlc.Profile, error)
	FindOrCreateMockUser(ctx context.Context) (sqlc.Profile, error)
}

// Middleware authenticates requests and injects the user into the context.
type Middleware struct {
	verifier    *Verifier
	profiles    Profiles
	log         *slog.Logger
	mockEnabled bool
}

// NewMiddleware builds the auth middleware. mockEnabled mirrors
// OIDC_MOCK_ENABLED && env != production.
func NewMiddleware(v *Verifier, p Profiles, log *slog.Logger, mockEnabled bool) *Middleware {
	return &Middleware{verifier: v, profiles: p, log: log, mockEnabled: mockEnabled}
}

// Optional attaches the authenticated user to the context when a valid token
// (or mock identity) is present, but never rejects the request. Enforcement is
// left to resolver/route guards (Require / RequireRole / RequireAuth).
func (m *Middleware) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := m.authenticate(r); user != nil {
			r = r.WithContext(WithUser(r.Context(), user))
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth rejects requests without an authenticated user (used for REST,
// e.g. POST /upload).
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := m.authenticate(r)
		if user == nil {
			http.Error(w, `{"statusCode":401,"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
	})
}

// authenticate resolves the user for an HTTP request or returns nil.
func (m *Middleware) authenticate(r *http.Request) *sqlc.Profile {
	return m.AuthenticateCreds(r.Context(), bearerToken(r), r.Header.Get("x-mock-sub"))
}

// AuthenticateCreds resolves a user from raw credentials. It is shared by the
// HTTP middleware (headers) and the WebSocket init func (connection payload),
// so subscriptions authenticate exactly like queries/mutations.
func (m *Middleware) AuthenticateCreds(ctx context.Context, bearer, mockSub string) *sqlc.Profile {
	if m.mockEnabled {
		if mockSub != "" {
			if p, err := m.profiles.FindBySub(ctx, mockSub); err == nil && p != nil {
				return p
			}
		}
		p, err := m.profiles.FindOrCreateMockUser(ctx)
		if err != nil {
			m.log.Error("mock user resolution failed", "error", err)
			return nil
		}
		return &p
	}

	if bearer == "" {
		return nil
	}
	sub, err := m.verifier.Verify(ctx, bearer)
	if err != nil {
		m.log.Debug("token verification failed", "error", err)
		return nil
	}
	p, err := m.profiles.FindOrCreateBySub(ctx, sub)
	if err != nil {
		m.log.Error("find-or-create profile failed", "error", err)
		return nil
	}
	return &p
}

// StripBearer extracts the token from an "Authorization: Bearer ..." value.
func StripBearer(authHeader string) string {
	const prefix = "Bearer "
	if len(authHeader) > len(prefix) && strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return authHeader[len(prefix):]
	}
	return authHeader
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}
