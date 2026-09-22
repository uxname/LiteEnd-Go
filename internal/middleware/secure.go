package middleware

import (
	"net/http"

	"github.com/unrolled/secure"
)

// SecureHeaders applies security headers analogous to @fastify/helmet:
// CSP, frame options, content-type nosniff, and HSTS in production.
func SecureHeaders(isProd bool) func(http.Handler) http.Handler {
	sec := secure.New(secure.Options{
		ContentTypeNosniff:    true,
		BrowserXssFilter:      true,
		FrameDeny:             true,
		ContentSecurityPolicy: "default-src 'self'",
		// HSTS only in production. The app sits behind a TLS-terminating proxy
		// and never sees r.TLS, and unrolled/secure sends HSTS only on requests
		// it believes are TLS — so it is forced rather than inferred from a
		// forwarded header a client could forge.
		STSSeconds:           stsSeconds(isProd),
		STSIncludeSubdomains: isProd,
		ForceSTSHeader:       isProd,
		IsDevelopment:        !isProd,
	})
	return sec.Handler
}

func stsSeconds(isProd bool) int64 {
	if isProd {
		return 31536000 // 1 year
	}
	return 0
}
