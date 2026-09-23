package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

// jwksServer serves a swappable JWKS and counts how often it is fetched.
type jwksServer struct {
	mu      sync.Mutex
	body    []byte
	fetches atomic.Int32
	url     string
}

func newJWKSServer(t *testing.T) *jwksServer {
	t.Helper()
	s := &jwksServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.fetches.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.body)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *jwksServer) serve(t *testing.T, keys ...*signingKey) {
	t.Helper()
	set := jose.JSONWebKeySet{}
	for _, k := range keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: k.priv.Public(), KeyID: k.kid, Algorithm: string(jose.RS256), Use: "sig"})
	}
	body, err := json.Marshal(set)
	require.NoError(t, err)
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
}

type signingKey struct {
	kid  string
	priv *rsa.PrivateKey
}

func newSigningKey(t *testing.T, kid string) *signingKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return &signingKey{kid: kid, priv: priv}
}

func (k *signingKey) sign(t *testing.T) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: k.priv, KeyID: k.kid}}, nil)
	require.NoError(t, err)
	signed, err := signer.Sign([]byte(`{"sub":"u1"}`))
	require.NoError(t, err)
	raw, err := signed.CompactSerialize()
	require.NoError(t, err)
	return raw
}

func testKeySet(url string, minInterval time.Duration) *cachedKeySet {
	return newCachedKeySet(url, &http.Client{Timeout: time.Second}, minInterval)
}

// go-oidc's RemoteKeySet refetches the JWKS for every token whose kid it does
// not know, so anonymous junk tokens became one outbound IdP request each.
func TestCachedKeySet_UnknownKeyIDsDoNotRefetch(t *testing.T) {
	t.Parallel()
	good, foreign := newSigningKey(t, "good"), newSigningKey(t, "attacker")
	srv := newJWKSServer(t)
	srv.serve(t, good)
	ks := testKeySet(srv.url, time.Hour)

	_, err := ks.VerifySignature(t.Context(), good.sign(t))
	require.NoError(t, err)
	for range 20 {
		_, err := ks.VerifySignature(t.Context(), foreign.sign(t))
		require.Error(t, err)
	}

	require.Equal(t, int32(1), srv.fetches.Load(), "one fetch, however many unknown kids")
}

// A bad signature under a known kid is not a reason to refetch at all.
func TestCachedKeySet_BadSignatureWithKnownKeyIDDoesNotRefetch(t *testing.T) {
	t.Parallel()
	good := newSigningKey(t, "good")
	impostor := &signingKey{kid: "good", priv: newSigningKey(t, "x").priv}
	srv := newJWKSServer(t)
	srv.serve(t, good)
	ks := testKeySet(srv.url, 0)

	_, err := ks.VerifySignature(t.Context(), good.sign(t))
	require.NoError(t, err)
	_, err = ks.VerifySignature(t.Context(), impostor.sign(t))
	require.Error(t, err)

	require.Equal(t, int32(1), srv.fetches.Load())
}

// A genuine key rotation is still picked up once the interval has passed.
func TestCachedKeySet_PicksUpRotatedKeyAfterInterval(t *testing.T) {
	t.Parallel()
	oldKey, newKey := newSigningKey(t, "old"), newSigningKey(t, "new")
	srv := newJWKSServer(t)
	srv.serve(t, oldKey)
	ks := testKeySet(srv.url, 50*time.Millisecond)
	_, err := ks.VerifySignature(t.Context(), oldKey.sign(t))
	require.NoError(t, err)

	srv.serve(t, newKey)
	time.Sleep(60 * time.Millisecond)
	_, err = ks.VerifySignature(t.Context(), newKey.sign(t))

	require.NoError(t, err)
	require.Equal(t, int32(2), srv.fetches.Load())
}

// With the IdP down from boot there are no keys yet, and every bearer used to
// trigger its own fetch. Retries back off even before the first load.
func TestCachedKeySet_BacksOffBeforeTheFirstLoad(t *testing.T) {
	t.Parallel()
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	ks := testKeySet(srv.URL, time.Hour)
	token := newSigningKey(t, "k").sign(t)

	for range 20 {
		_, err := ks.VerifySignature(t.Context(), token)
		require.Error(t, err)
	}

	require.Equal(t, int32(1), fetches.Load())
}

// A key the IdP has removed (rotated out, or revoked after a leak) must stop
// verifying, even while tokens keep naming its kid.
func TestCachedKeySet_DropsARemovedKeyAfterMaxAge(t *testing.T) {
	t.Parallel()
	oldKey, newKey := newSigningKey(t, "old"), newSigningKey(t, "new")
	srv := newJWKSServer(t)
	srv.serve(t, oldKey)
	ks := testKeySet(srv.url, time.Hour)
	ks.maxAge, ks.retryEvery = 50*time.Millisecond, 10*time.Millisecond
	_, err := ks.VerifySignature(t.Context(), oldKey.sign(t))
	require.NoError(t, err)

	srv.serve(t, newKey)
	time.Sleep(60 * time.Millisecond)
	_, err = ks.VerifySignature(t.Context(), oldKey.sign(t))

	require.Error(t, err)
}
