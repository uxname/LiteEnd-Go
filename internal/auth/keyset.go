package auth

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"github.com/uxname/liteend-go/internal/config"
)

// maxJWKSBytes bounds a JWKS response; real ones are a few KiB.
const maxJWKSBytes = 1 << 20

// cachedKeySet is an oidc.KeySet that verifies against the last fetched JWKS
// and refetches it only when a token names a key id it does not know — and
// then at most once per minInterval. go-oidc's RemoteKeySet refetches on every
// such miss, which turned each anonymous token with an invented kid into one
// outbound request to the IdP. A bad signature under a known kid never
// triggers a fetch: the key set is not what is wrong.
//
// ponytail: one mutex also serialises the (rare, time-bounded) fetch; a
// singleflight with lock-free reads if verification ever contends on it.
type cachedKeySet struct {
	uri         string
	client      *http.Client
	minInterval time.Duration // between refetches triggered by a kid miss
	retryEvery  time.Duration // between attempts while no JWKS has loaded yet
	maxAge      time.Duration // a loaded JWKS is re-read after this, so removed keys stop verifying

	mu        sync.Mutex
	keys      *oidc.StaticKeySet
	kids      map[string]bool
	fetchedAt time.Time // last attempt
	loadedAt  time.Time // last success
}

func newCachedKeySet(uri string, client *http.Client, minInterval time.Duration) *cachedKeySet {
	return &cachedKeySet{
		uri: uri, client: client, minInterval: minInterval,
		retryEvery: config.OIDCJWKSRetryInterval, maxAge: config.OIDCJWKSMaxAge,
	}
}

// VerifySignature implements oidc.KeySet.
func (s *cachedKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	kid := keyID(jwt)

	s.mu.Lock()
	now := time.Now()
	missing := s.keys == nil || kid == "" || !s.kids[kid]
	stale := s.keys != nil && now.Sub(s.loadedAt) >= s.maxAge
	// Before the first load there is nothing to verify with, but the IdP may
	// be down from boot: retry on a short timer instead of once per token.
	wait := s.minInterval
	if s.keys == nil {
		wait = s.retryEvery
	}
	since := now.Sub(s.fetchedAt)
	// A stale set is re-read once per maxAge anyway, so only a failing
	// re-read is spaced out (by retryEvery), not held to minInterval.
	if (missing && since >= wait) || (stale && since >= s.retryEvery) {
		s.fetchedAt = now
		// A failed refresh keeps the keys already loaded: an IdP outage must
		// not log everyone out.
		_ = s.refresh(ctx)
	}
	keys := s.keys
	s.mu.Unlock()
	if keys == nil {
		return nil, errors.New("jwks not loaded yet")
	}

	payload, err := keys.VerifySignature(ctx, jwt)
	if err != nil {
		return nil, fmt.Errorf("verify signature: %w", err)
	}
	return payload, nil
}

// refresh fetches the JWKS. Called with mu held.
func (s *cachedKeySet) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.uri, nil)
	if err != nil {
		return fmt.Errorf("build jwks request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	var pub []crypto.PublicKey
	kids := map[string]bool{}
	for _, k := range set.Keys {
		if k.Use == "enc" || !k.IsPublic() {
			continue
		}
		pub = append(pub, k.Key)
		kids[k.KeyID] = true
	}
	if len(pub) == 0 {
		return errors.New("jwks has no signing keys")
	}
	s.keys, s.kids, s.loadedAt = &oidc.StaticKeySet{PublicKeys: pub}, kids, time.Now()
	return nil
}

// keyID reads the kid from a compact JWS header, "" when absent or unparsable
// (the signature check that follows rejects the unparsable ones).
func keyID(jwt string) string {
	sig, err := jose.ParseSigned(jwt, []jose.SignatureAlgorithm{jose.RS256, jose.ES384})
	if err != nil || len(sig.Signatures) == 0 {
		return ""
	}
	return sig.Signatures[0].Header.KeyID
}
