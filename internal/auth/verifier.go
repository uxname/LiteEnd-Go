package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/uxname/liteend-go/internal/config"
)

// Verifier validates bearer tokens against the OIDC issuer's JWKS, checking
// signature, issuer, audience and expiry. Mirrors the passport-jwt strategy.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier builds a Verifier using a remote JWKS key set.
func NewVerifier(ctx context.Context, cfg *config.Config) *Verifier {
	keySet := oidc.NewRemoteKeySet(ctx, cfg.OIDCJWKSURI)
	v := oidc.NewVerifier(cfg.OIDCIssuer, keySet, &oidc.Config{
		ClientID:             cfg.OIDCAudience,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES384},
	})
	return &Verifier{verifier: v}
}

// claims is the subset of token claims we consume.
type claims struct {
	Sub string `json:"sub"`
}

// Verify validates a raw bearer token and returns the subject (sub).
func (v *Verifier) Verify(ctx context.Context, rawToken string) (string, error) {
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", fmt.Errorf("verify token: %w", err)
	}
	var c claims
	if err := tok.Claims(&c); err != nil {
		return "", fmt.Errorf("parse claims: %w", err)
	}
	if c.Sub == "" {
		return "", errors.New("token has no subject (sub)")
	}
	return c.Sub, nil
}
