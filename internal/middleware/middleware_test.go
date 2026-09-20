package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
// readable by clientip.ClientIP, which is what keys the bucket.
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

// C8: HAProxy's "option forwardfor" appends its entry as a SECOND
// X-Forwarded-For line instead of extending the first, and Header.Get reads only
// the first line — the one the client wrote in full. Every line is part of the
// same chain, or a client sends one comma-less header and picks its own bucket
// again.
func TestC8_RepeatedXForwardedForLinesFormOneChain(t *testing.T) {
	t.Parallel()
	var key string
	h := RealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { key = rateKey(r) }))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.RemoteAddr = "172.20.0.5:44120"            // the proxy's socket address
	req.Header.Add("X-Forwarded-For", "1.1.1.1")   // the line the client sent
	req.Header.Add("X-Forwarded-For", "127.0.0.1") // the line our proxy appended
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "rl:auth:127.0.0.1", key, "the trusted entry is the last one across ALL header lines, not the last one of the first line")
}

// C8: Azure Application Gateway and Front Door append chain entries WITH a port
// ("10.0.0.1:5678", IPv6 bracketed as "[2001:db8::1]:443"). Refusing to parse
// those falls back to the socket address — the proxy's — which drops every
// client on earth into one rate-limit bucket.
func TestC8_ForwardedEntryWithPortResolvesToItsAddress(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		chain string
		want  string
	}{
		"IPv4 with port": {chain: "1.2.3.4, 10.0.0.1:5678", want: "10.0.0.1"},
		"IPv6 with port": {chain: "1.2.3.4, [2001:db8::1]:443", want: "2001:db8::1"},
		"bare IPv6":      {chain: "1.2.3.4, ::1", want: "::1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var key string
			h := RealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { key = rateKey(r) }))

			req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
			req.RemoteAddr = "172.20.0.5:44120"
			req.Header.Set("X-Forwarded-For", tc.chain)
			h.ServeHTTP(httptest.NewRecorder(), req)

			require.Equal(t, "rl:auth:"+tc.want, key, "the port must be stripped off the entry, not send the whole request into the proxy's bucket")
		})
	}
}

// C8: a chain entry may carry a bracketed IPv6 address with NO port ("[::1]"),
// and that shape passes neither check on its own: the brackets defeat ParseIP
// and the missing port defeats SplitHostPort. Reading it as garbage falls back
// to the socket address — the proxy's — which is the one bucket for everyone
// again.
func TestC8_BracketedIPv6WithoutPortResolvesToItsAddress(t *testing.T) {
	t.Parallel()
	var key string
	h := RealIP(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { key = rateKey(r) }))

	req := httptest.NewRequest(http.MethodGet, "/graphql", nil)
	req.RemoteAddr = "172.20.0.5:44120"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, [2001:db8::1]")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, "rl:auth:2001:db8::1", key, "the brackets must be stripped off the entry, not make it unparseable")
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

// BasicAuth is the only guard on the dev pages, so every way of getting past it
// is asserted: a wrong user must fail exactly like a wrong password (the
// constant-time compare covers both), and a rejection must not leak through to
// the handler behind it.
func TestBasicAuth_RejectsEveryCredentialMismatch(t *testing.T) {
	t.Parallel()
	const realm, user, pass = "liteend dev tools", "admin", "s3cret"

	for name, tc := range map[string]struct {
		setAuth  bool
		user     string
		pass     string
		wantCode int
	}{
		"correct credentials": {setAuth: true, user: user, pass: pass, wantCode: http.StatusOK},
		"no Authorization":    {wantCode: http.StatusUnauthorized},
		"wrong password":      {setAuth: true, user: user, pass: "nope", wantCode: http.StatusUnauthorized},
		"wrong user":          {setAuth: true, user: "root", pass: pass, wantCode: http.StatusUnauthorized},
		"empty credentials":   {setAuth: true, wantCode: http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			called := false
			h := BasicAuth(realm, user, pass)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/dev", nil)
			if tc.setAuth {
				req.SetBasicAuth(tc.user, tc.pass)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			require.Equal(t, tc.wantCode, rec.Code)
			if tc.wantCode == http.StatusOK {
				require.True(t, called, "the guarded handler must run once the credentials match")
				return
			}

			require.False(t, called, "a rejected request must never reach the guarded handler")
			require.Equal(t, `Basic realm="`+realm+`"`, rec.Header().Get("WWW-Authenticate"),
				"without the challenge header a browser never offers the login prompt")
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var payload struct {
				StatusCode int    `json:"statusCode"`
				Message    string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload),
				"the 401 body is the shared httperr envelope, not an empty response")
			require.Equal(t, http.StatusUnauthorized, payload.StatusCode)
			require.Equal(t, "Unauthorized", payload.Message)
		})
	}
}
