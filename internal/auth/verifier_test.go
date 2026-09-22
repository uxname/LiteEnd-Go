package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/config"
)

// These tests exercise the real token check — signature, issuer, audience and
// expiry — with no mock auth anywhere. The keys come from a local JWKS served by
// httptest, so nothing leaves the machine.

const (
	testIssuer   = "https://issuer.test"
	testAudience = "test-audience"
	testKeyID    = "test-key"
)

// newIssuer starts a local JWKS endpoint and returns a verifier pointed at it
// plus a sign function that mints tokens with the matching private key.
func newIssuer(t *testing.T) (*auth.Verifier, func(t *testing.T, claims map[string]any) string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       key.Public(),
		KeyID:     testKeyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}}
	// Encoded once, outside the handler: a failed assertion inside one would be
	// reported from the wrong goroutine (and testifylint says so).
	jwksJSON, err := json.Marshal(jwks)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksJSON)
	}))
	t.Cleanup(srv.Close)

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: testKeyID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	require.NoError(t, err)

	sign := func(t *testing.T, claims map[string]any) string {
		t.Helper()
		payload, err := json.Marshal(claims)
		require.NoError(t, err)
		signed, err := signer.Sign(payload)
		require.NoError(t, err)
		raw, err := signed.CompactSerialize()
		require.NoError(t, err)
		return raw
	}

	verifier := auth.NewVerifier(t.Context(), &config.Config{
		OIDCIssuer:   testIssuer,
		OIDCAudience: testAudience,
		OIDCJWKSURI:  srv.URL,
	})
	return verifier, sign
}

// claims returns a valid claim set, which each test then breaks in one way.
func claims(expiry time.Time) map[string]any {
	return map[string]any{
		"iss": testIssuer,
		"aud": testAudience,
		"sub": "user-1",
		"iat": time.Now().Add(-time.Minute).Unix(),
		"exp": expiry.Unix(),
	}
}

func TestVerify_ValidToken(t *testing.T) {
	t.Parallel()
	verifier, sign := newIssuer(t)

	sub, _, err := verifier.Verify(t.Context(), sign(t, claims(time.Now().Add(time.Hour))))
	require.NoError(t, err)
	require.Equal(t, "user-1", sub)
}

func TestVerify_Rejects(t *testing.T) {
	t.Parallel()
	verifier, sign := newIssuer(t)

	expired := claims(time.Now().Add(-time.Hour))

	foreignIssuer := claims(time.Now().Add(time.Hour))
	foreignIssuer["iss"] = "https://evil.test"

	foreignAudience := claims(time.Now().Add(time.Hour))
	foreignAudience["aud"] = "someone-elses-api"

	noSubject := claims(time.Now().Add(time.Hour))
	delete(noSubject, "sub")

	for name, c := range map[string]map[string]any{
		"expired token":         expired,
		"foreign issuer":        foreignIssuer,
		"foreign audience":      foreignAudience,
		"token without subject": noSubject,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := verifier.Verify(t.Context(), sign(t, c))
			require.Error(t, err)
		})
	}
}

func TestVerify_UnsignedGarbageRejected(t *testing.T) {
	t.Parallel()
	verifier, _ := newIssuer(t)

	_, _, err := verifier.Verify(t.Context(), "not-a-token")
	require.Error(t, err)
}

// A token signed by a different key must not pass, or the JWKS check is theatre.
func TestVerify_ForeignSignatureRejected(t *testing.T) {
	t.Parallel()
	verifier, _ := newIssuer(t)
	_, otherSign := newIssuer(t)

	_, _, err := verifier.Verify(t.Context(), otherSign(t, claims(time.Now().Add(time.Hour))))
	require.Error(t, err)
}

// config.OIDCClockSkew is the tolerated drift between this host's clock and the
// issuer's: just inside it a freshly expired token still works, just outside it
// does not.
func TestVerify_ClockSkewTolerance(t *testing.T) {
	t.Parallel()
	verifier, sign := newIssuer(t)

	withinSkew := sign(t, claims(time.Now().Add(-config.OIDCClockSkew/2)))
	sub, _, err := verifier.Verify(t.Context(), withinSkew)
	require.NoError(t, err, "a token expired inside the skew tolerance is accepted")
	require.Equal(t, "user-1", sub)

	beyondSkew := sign(t, claims(time.Now().Add(-config.OIDCClockSkew-time.Minute)))
	_, _, err = verifier.Verify(t.Context(), beyondSkew)
	require.Error(t, err, "past the tolerance the same token is rejected")
}
