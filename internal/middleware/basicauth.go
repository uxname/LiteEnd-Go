package middleware

import (
	"crypto/subtle"
	"net/http"
)

// BasicAuth guards a route with HTTP Basic Auth using constant-time comparison.
// Used to keep the app's own dev pages (/dev, /playground, /swagger) from
// anonymous access — mirroring the auth on the external admin dashboards.
func BasicAuth(realm, user, pass string) func(http.Handler) http.Handler {
	userB, passB := []byte(user), []byte(pass)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			if !ok ||
				subtle.ConstantTimeCompare([]byte(u), userB) != 1 ||
				subtle.ConstantTimeCompare([]byte(p), passB) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
				http.Error(w, `{"statusCode":401,"message":"Unauthorized"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
