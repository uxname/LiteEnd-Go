// Package auth handles OIDC/JWT verification, the authenticated-user context,
// HTTP middleware, and role-based guards used by resolvers and REST handlers.
package auth

import (
	"context"
	"errors"

	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// ErrUnauthenticated is returned when no authenticated user is present.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrForbidden is returned when the user lacks a required role.
var ErrForbidden = errors.New("forbidden")

type ctxKey struct{}

var userKey ctxKey //nolint:gochecknoglobals // unique context-key sentinel (idiomatic Go)

// WithUser returns a copy of ctx carrying the authenticated profile.
func WithUser(ctx context.Context, p *sqlc.Profile) context.Context {
	return context.WithValue(ctx, userKey, p)
}

// UserFromContext returns the authenticated profile, if any.
func UserFromContext(ctx context.Context) (*sqlc.Profile, bool) {
	p, ok := ctx.Value(userKey).(*sqlc.Profile)
	return p, ok && p != nil
}

// Require returns the authenticated profile or ErrUnauthenticated.
func Require(ctx context.Context) (*sqlc.Profile, error) {
	if p, ok := UserFromContext(ctx); ok {
		return p, nil
	}
	return nil, ErrUnauthenticated
}

// RequireRole returns the profile if it holds the given role, else an error.
func RequireRole(ctx context.Context, role sqlc.ProfileRole) (*sqlc.Profile, error) {
	p, err := Require(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range p.Roles {
		if r == role {
			return p, nil
		}
	}
	return nil, ErrForbidden
}
