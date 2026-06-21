package middleware

import (
	"net/http"
	"strings"
)

// RealIP rewrites r.RemoteAddr from X-Forwarded-For / X-Real-IP, mirroring the
// TypeScript Fastify trustProxy:true behaviour.
//
// SECURITY: only enable behind a trusted reverse proxy that sets these headers;
// clients can otherwise spoof them. This is the documented deployment model
// (the app sits behind a proxy), matching the original NestJS setup.
func RealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := clientIPFromHeaders(r); ip != "" {
			r.RemoteAddr = ip
		}
		next.ServeHTTP(w, r)
	})
}

func clientIPFromHeaders(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	return ""
}
