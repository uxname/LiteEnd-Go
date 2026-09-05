package middleware

import (
	"net"
	"net/http"
	"strings"
)

// RealIP rewrites r.RemoteAddr with the real client address, taken from
// X-Forwarded-For trustedHops entries FROM THE RIGHT.
//
// SECURITY: X-Forwarded-For is written by the client and only appended to by the
// proxies in front of us, so only the last trustedHops entries are trustworthy —
// everything to their left is whatever the client typed. Taking the leftmost
// entry (the "original client", which is what this middleware used to do) hands
// every caller a free rate-limit bucket per forged header.
//
// trustedHops is TRUSTED_PROXY_HOPS: the number of reverse proxies in front of
// this app. Zero means there are none, so both forwarding headers are
// client-controlled and are ignored entirely — the socket address wins. A chain
// shorter than trustedHops, or an entry that is not an IP, cannot have come from
// our proxies either, and falls back to the socket address rather than to the
// client's value.
func RealIP(trustedHops int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := forwardedClientIP(r, trustedHops); ip != "" {
				// Keep RemoteAddr's documented "host:port" shape: ClientIP and
				// chi's loggers SplitHostPort it, and on failure they fall back
				// to the raw string — which is how a bare address used to smuggle
				// a whole forged header in as a rate-limit key.
				if _, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
					r.RemoteAddr = net.JoinHostPort(ip, port)
				} else {
					r.RemoteAddr = ip
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP is the address of the caller, without a port: RemoteAddr after RealIP
// has resolved it. Register RealIP before any handler that calls this, or it
// returns the socket peer (a proxy) instead of the client.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// forwardedClientIP returns the forwarded address that trustedHops proxies vouch
// for, or "" when nothing in the headers can be trusted.
func forwardedClientIP(r *http.Request, trustedHops int) string {
	if trustedHops <= 0 {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		entries := strings.Split(xff, ",")
		i := len(entries) - trustedHops
		if i < 0 {
			return ""
		}
		return validIP(entries[i])
	}
	// X-Real-IP carries no chain to count, so it is believed only on the word of
	// a trusted proxy — the same forgery as X-Forwarded-For otherwise.
	return validIP(r.Header.Get("X-Real-IP"))
}

func validIP(s string) string {
	s = strings.TrimSpace(s)
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}
