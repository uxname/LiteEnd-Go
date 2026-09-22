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
	minInterval time.Duration

	mu        sync.Mutex
	keys      *oidc.StaticKeySet
	kids      map[string]bool
	fetchedAt time.Time
}

func newCachedKeySet(uri string, client *http.Client, minInterval time.Duration) *cachedKeySet {
	return &cachedKeySet{uri: uri, client: client, minInterval: minInterval}
}

// VerifySignature implements oidc.KeySet.
func (s *cachedKeySet) VerifySignature(ctx context.Context, jwt string) ([]byte, error) {
	kid := keyID(jwt)

	s.mu.Lock()
	missing := s.keys == nil || kid == "" || !s.kids[kid]
	if missing && (s.keys == nil || time.Since(s.fetchedAt) >= s.minInterval) {
		s.fetchedAt = time.Now()
		if err := s.refresh(ctx); err != nil && s.keys == nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	keys := s.keys
	s.mu.Unlock()

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
	s.keys, s.kids = &oidc.StaticKeySet{PublicKeys: pub}, kids
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
