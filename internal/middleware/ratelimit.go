package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/config"
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
				// Fail-open on limiter errors (Redis down) — availability over throttling.
				next.ServeHTTP(w, r)
				return
			}
			if res.Allowed <= 0 {
				w.Header().Set("Retry-After", res.RetryAfter.String())
				http.Error(w, `{"error":"Too Many Requests"}`, http.StatusTooManyRequests)
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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
