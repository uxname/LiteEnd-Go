package middleware

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/httperr"
	"github.com/uxname/liteend-go/internal/logger"
)

// RateLimit returns a Redis-backed (GCRA) rate-limiting middleware.
// Mirrors @fastify/rate-limit: RateLimitMax requests per RateLimitWindow.
// For /upload and /graphql the key is "auth:{ip}", otherwise the bare IP,
// matching the TypeScript keyGenerator.
func RateLimit(rdb *redis.Client) func(http.Handler) http.Handler {
	limiter := redis_rate.NewLimiter(rdb)
	limit := redis_rate.Limit{
		Rate:   config.RateLimitMax,
		Period: config.RateLimitWindow,
		Burst:  config.RateLimitMax,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := rateKey(r)
			res, err := limiter.Allow(r.Context(), key, limit)
			if err != nil {
				// Fail-open on limiter errors (Redis down) — availability over
				// throttling. Say so in the log: silently dropping the error made
				// "rate limiting is off" indistinguishable from "nobody hit a limit".
				// One line per request while Redis is down is the intended volume —
				// an unprotected surface should be noisy.
				logger.From(r.Context()).Warn("rate limiter unavailable, allowing request", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if res.Allowed <= 0 {
				// RFC 9110: Retry-After is delay-seconds, not a Go duration string
				// ("1m39s" is unparseable, so clients retry immediately).
				retryAfter := int(math.Ceil(res.RetryAfter.Seconds()))
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				httperr.Write(w, http.StatusTooManyRequests, "Too Many Requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func rateKey(r *http.Request) string {
	ip := clientIP(r)
	p := r.URL.Path
	if strings.HasPrefix(p, "/upload") || strings.HasPrefix(p, "/graphql") {
		return "rl:auth:" + ip
	}
	return "rl:" + ip
}
