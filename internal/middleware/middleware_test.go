package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// C8: behind one trusted proxy the client address is the entry our proxy
// APPENDED — the last one — not the prefix the client sent. The rate-limit key
// is asserted (not just RemoteAddr) because that is the thing an attacker was
// escaping: with the old "first entry" rule any client picked its own bucket.
func TestC8_RateKeyUsesProxyAppendedAddress(t *testing.T) {
	t.Parallel()
	var key, remote string
	h := RealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		key, remote = rateKey(r), r.RemoteAddr
	}))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.RemoteAddr = "172.20.0.5:44120" // the proxy's socket address
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "rl:auth:10.0.0.1", key, "the bucket keys on what the proxy saw, not on what the client claimed")
	require.Equal(t, "10.0.0.1:44120", remote, `RemoteAddr keeps its documented "host:port" shape, or every SplitHostPort reader silently falls back to the raw string`)
}

// C8 (negative): a client that reaches the app directly controls both headers,
// so with no trusted proxy configured they are ignored — forging them does not
// buy a second bucket.
func TestC8_ForgedHeadersWithoutTrustedProxyGetNoNewBucket(t *testing.T) {
	t.Parallel()
	var key string
	h := RealIP(0)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { key = rateKey(r) }))

	for _, forged := range []string{"1.2.3.4", "1.2.3.5"} {
		req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
		req.RemoteAddr = "203.0.113.9:5555"
		req.Header.Set("X-Forwarded-For", forged)
		req.Header.Set("X-Real-IP", forged)
		h.ServeHTTP(httptest.NewRecorder(), req)

		require.Equal(t, "rl:auth:203.0.113.9", key, "forged %q must not move the request into another bucket", forged)
	}
}

// C8: a chain shorter than the trusted prefix cannot have been written by our
// proxies, so it is worthless — fall back to the socket address, never to the
// client-supplied value.
func TestC8_ChainShorterThanTrustedHopsFallsBackToSocket(t *testing.T) {
	t.Parallel()
	var key string
	h := RealIP(2)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { key = rateKey(r) }))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "rl:auth:203.0.113.9", key)
}

// C8: X-Real-IP carries no chain, so it can only ever be believed on the word of
// a trusted proxy. Trust is explicit: TRUSTED_PROXY_HOPS > 0.
func TestC8_XRealIPNeedsTrustedProxy(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		hops int
		want string
	}{
		"no trusted proxy":  {hops: 0, want: "203.0.113.9:5555"},
		"one trusted proxy": {hops: 1, want: "9.9.9.9:5555"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got string
			h := RealIP(tc.hops)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "203.0.113.9:5555"
			req.Header.Set("X-Real-IP", "9.9.9.9")
			h.ServeHTTP(httptest.NewRecorder(), req)

			require.Equal(t, tc.want, got)
		})
	}
}

// C8: garbage from a broken proxy must not become a rate-limit key.
func TestC8_NonIPForwardedEntryFallsBackToSocket(t *testing.T) {
	t.Parallel()
	var got string
	h := RealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, not-an-ip")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "203.0.113.9:5555", got)
}

// C8: some requests reach the middleware with a portless RemoteAddr (synthetic
// requests, health probes). The forwarded address must still win — and still be
// readable by ClientIP, which is what keys the bucket.
func TestC8_PortlessRemoteAddrStillResolves(t *testing.T) {
	t.Parallel()
	var key string
	h := RealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { key = rateKey(r) }))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.RemoteAddr = "172.20.0.5"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "rl:auth:10.0.0.1", key)
}

func TestRealIP_NoHeadersKeepsRemoteAddr(t *testing.T) {
	t.Parallel()
	var got string
	h := RealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "10.0.0.1:1234", got)
}

func TestRecoverer_PanicReturns500(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)
	h := Recoverer(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "Internal Server Error")
}

func TestBodyLimit_RejectsOversizedBody(t *testing.T) {
	t.Parallel()
	var readErr error
	h := BodyLimit(8)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 100)))
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Error(t, readErr, "reading past the limit must fail")
}

func TestSecureHeaders_SetsHardeningHeaders(t *testing.T) {
	t.Parallel()
	h := SecureHeaders(false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))
}
