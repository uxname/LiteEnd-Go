package auth

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/httperr"
	"github.com/uxname/liteend-go/internal/logger"
)

// userContext attaches the authenticated user to ctx and enriches the
// request-scoped logger with user_id, so every downstream log line is
// attributable to the user (alongside the request_id added by middleware).
func userContext(ctx context.Context, user *sqlc.Profile) context.Context {
	ctx = WithUser(ctx, user)
	return logger.Into(ctx, logger.From(ctx).With(slog.Int("user_id", int(user.ID))))
}

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
		// Optional never rejects (enforcement is left to resolver guards): a
		// provider outage simply yields an anonymous request, already logged below.
		if user, _ := m.resolve(r.Context(), bearerToken(r), r.Header.Get("x-mock-sub")); user != nil {
			r = r.WithContext(userContext(r.Context(), user))
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth rejects requests without an authenticated user (used for REST,
// e.g. POST /upload).
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, providerDown := m.resolve(r.Context(), bearerToken(r), r.Header.Get("x-mock-sub"))
		if providerDown {
			// The token may well be valid — we just can't reach the OIDC provider
			// to check it. 503 (not 401) tells the client to retry rather than
			// re-authenticate.
			httperr.Write(w, http.StatusServiceUnavailable, "Authentication provider unavailable")
			return
		}
		if user == nil {
			httperr.Write(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(userContext(r.Context(), user)))
	})
}

// AuthenticateCreds resolves a user from raw credentials, returning nil when
// authentication fails for any reason. Shared by the HTTP middleware (headers)
// and the WebSocket init func (connection payload), so subscriptions
// authenticate exactly like queries/mutations.
func (m *Middleware) AuthenticateCreds(ctx context.Context, bearer, mockSub string) *sqlc.Profile {
	user, _ := m.resolve(ctx, bearer, mockSub)
	return user
}

// resolve authenticates raw credentials and reports whether failure was due to
// the OIDC provider being unreachable (vs. a missing/invalid token), so callers
// can return 503 instead of a misleading 401.
func (m *Middleware) resolve(ctx context.Context, bearer, mockSub string) (user *sqlc.Profile, providerDown bool) {
	if m.mockEnabled {
		if mockSub != "" {
			if p, err := m.profiles.FindBySub(ctx, mockSub); err == nil && p != nil {
				return p, false
			}
		}
		p, err := m.profiles.FindOrCreateMockUser(ctx)
		if err != nil {
			m.log.Error("mock user resolution failed", "error", err)
			return nil, false
		}
		return &p, false
	}

	if bearer == "" {
		return nil, false
	}
	sub, err := m.verifier.Verify(ctx, bearer)
	if err != nil {
		if isProviderUnavailable(err) {
			m.log.Warn("oidc provider unavailable during token verification", "error", err)
			return nil, true
		}
		// A failed verification is a security-relevant event; log at Warn so it is
		// visible at production log levels (not just Debug).
		m.log.Warn("token verification failed", "error", err)
		return nil, false
	}
	p, err := m.profiles.FindOrCreateBySub(ctx, sub)
	if err != nil {
		m.log.Error("find-or-create profile failed", "error", err)
		return nil, false
	}
	return &p, false
}

// isProviderUnavailable reports whether err indicates the OIDC provider could
// not be reached (network/timeout), as opposed to a genuinely invalid token.
func isProviderUnavailable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// parseBearer splits an "Authorization: Bearer ..." value into its token.
// ok is false when the value carries no (case-insensitive) "Bearer " prefix.
func parseBearer(authHeader string) (token string, ok bool) {
	const prefix = "Bearer "
	if len(authHeader) > len(prefix) && strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return authHeader[len(prefix):], true
	}
	return "", false
}

// StripBearer extracts the token from an "Authorization: Bearer ..." value,
// returning the input unchanged when it has no Bearer prefix (the WebSocket
// init payload may already carry the raw token).
func StripBearer(authHeader string) string {
	if token, ok := parseBearer(authHeader); ok {
		return token
	}
	return authHeader
}

// bearerToken returns the request's bearer token, or "" when the Authorization
// header is missing or lacks the Bearer prefix.
func bearerToken(r *http.Request) string {
	token, _ := parseBearer(r.Header.Get("Authorization"))
	return token
}
