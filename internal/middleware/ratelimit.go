package middleware

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/clientip"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/httperr"
	appredis "github.com/uxname/liteend-go/internal/redis"
)

// Allower spends one event of a named budget; *Limiter is the implementation.
type Allower interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration)
}

// RateLimit returns a Redis-backed (GCRA) rate-limiting middleware.
// Mirrors @fastify/rate-limit: RateLimitMax requests per RateLimitWindow.
// For /upload and /graphql the key is "auth:{ip}", otherwise the bare IP,
// matching the TypeScript keyGenerator.
func RateLimit(rdb *redis.Client) func(http.Handler) http.Handler {
	return throttle(appredis.NewLimiter(rdb, config.RateLimitMax, config.RateLimitWindow), RateKey)
}

// PerIP charges every request to the client address's own budget under scope,
// separate from the general one (e.g. a tighter limit on a password prompt).
func PerIP(a Allower, scope string) func(http.Handler) http.Handler {
	return throttle(a, func(r *http.Request) string { return "rl:" + scope + ":" + clientip.ClientIP(r) })
}

// throttle answers 429 once key(r)'s budget is spent.
func throttle(a Allower, key func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, retry := a.Allow(r.Context(), key(r))
			if !allowed {
				// RFC 9110: Retry-After is delay-seconds, not a Go duration string
				// ("1m39s" is unparseable, so clients retry immediately).
				retryAfter := int(math.Ceil(retry.Seconds()))
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				httperr.Write(w, http.StatusTooManyRequests, "Too Many Requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateKey is the budget a request is charged to. The GraphQL handler reuses it
// so operations sent over a WebSocket draw from the same budget as HTTP ones.
func RateKey(r *http.Request) string {
	ip := clientip.ClientIP(r)
	p := r.URL.Path
	if strings.HasPrefix(p, "/upload") || strings.HasPrefix(p, "/graphql") {
		return "rl:auth:" + ip
	}
	return "rl:" + ip
}
