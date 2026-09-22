package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/uxname/liteend-go/internal/httperr"
)

// BasicAuth guards a route with HTTP Basic Auth using constant-time comparison.
// Used to keep the app's own dev pages (/dev, /playground, /docs) from
// anonymous access — mirroring the auth on the external admin dashboards.
//
// Both fields are compared as fixed-length digests and both comparisons always
// run: ConstantTimeCompare returns early on a length mismatch, and a || skipped
// the password check on a wrong user, so timing told a guesser the user name
// and the password length.
func BasicAuth(realm, user, pass string) func(http.Handler) http.Handler {
	userD, passD := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			gotU, gotP := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
			match := subtle.ConstantTimeCompare(gotU[:], userD[:]) & subtle.ConstantTimeCompare(gotP[:], passD[:])
			if !ok || match != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
				httperr.Write(w, http.StatusUnauthorized, "Unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
