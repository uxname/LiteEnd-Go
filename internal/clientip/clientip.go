// Package clientip resolves the caller's address from a request.
//
// It is a cross-cutting helper on purpose: both the transport layer (rate
// limiting) and domain code (the uploader address written to the database) need
// the same answer, and a second copy is how the two drift apart. It is declared
// in commonComponents in .go-arch-lint.yml, so any layer may import it.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP is the address of the caller, without a port: RemoteAddr after
// middleware.RealIP has resolved it against TRUSTED_PROXY_HOPS.
//
// SECURITY: it reads RemoteAddr and nothing else. Never add a header read here.
// An earlier version of the upload copy preferred the leftmost X-Forwarded-For
// entry, which let any caller write its own uploader_ip straight into the
// database, and hands every caller a free rate-limit bucket per forged header.
// Register RealIP before any handler that calls this, or it returns the socket
// peer (a proxy) instead of the client.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.Trim(r.RemoteAddr, "[]")
	}
	return host
}
