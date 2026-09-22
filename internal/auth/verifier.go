package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/uxname/liteend-go/internal/config"
)

// Verifier validates bearer tokens against the OIDC issuer's JWKS, checking
// signature, issuer, audience and expiry. Mirrors the passport-jwt strategy.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier builds a Verifier over the issuer's JWKS (see cachedKeySet).
func NewVerifier(cfg *config.Config) *Verifier {
	// A timeout-bounded client, so a JWKS fetch (including the lazy refreshes on
	// later Verify calls) can never hang and stall every authenticated request.
	httpClient := &http.Client{Timeout: config.OIDCHTTPTimeout}
	keySet := newCachedKeySet(cfg.OIDCJWKSURI, httpClient, config.OIDCJWKSRefreshMinInterval)
	v := oidc.NewVerifier(cfg.OIDCIssuer, keySet, &oidc.Config{
		ClientID:             cfg.OIDCAudience,
		SupportedSigningAlgs: []string{oidc.RS256, oidc.ES384},
		// Expiry is checked against a clock held config.OIDCClockSkew behind the
		// real one, which is how the library expresses "tolerate that much drift":
		// a token that expired a few seconds ago still passes, one expired past
		// the tolerance does not.
		Now: func() time.Time { return time.Now().Add(-config.OIDCClockSkew) },
	})
	return &Verifier{verifier: v}
}

// claims is the subset of token claims we consume. Nonce and AtHash are read
// for presence only: they appear in ID tokens, never in access tokens.
type claims struct {
	Sub    string           `json:"sub"`
	Nonce  *json.RawMessage `json:"nonce"`
	AtHash *json.RawMessage `json:"at_hash"`
}

// Verify validates a raw bearer token and returns its subject (sub) and expiry.
// The expiry lets long-lived connections (WebSocket) end when the token does.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (sub string, expiry time.Time, err error) {
	tok, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("verify token: %w", err)
	}
	var c claims
	if err := tok.Claims(&c); err != nil {
		return "", time.Time{}, fmt.Errorf("parse claims: %w", err)
	}
	// An ID token is issuer-signed too, and carries our audience whenever
	// OIDC_AUDIENCE is (wrongly) the SPA client id — but it proves a login to
	// the client, it grants nothing to this API.
	if c.Nonce != nil || c.AtHash != nil {
		return "", time.Time{}, errors.New("token is an ID token (nonce/at_hash), not an access token")
	}
	if c.Sub == "" {
		return "", time.Time{}, errors.New("token has no subject (sub)")
	}
	return c.Sub, tok.Expiry, nil
}
