package middleware

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/clientip"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/httperr"
	"github.com/uxname/liteend-go/internal/logger"
)

// Limiter is a Redis-backed (GCRA) budget of rate events per period. It is the
// one definition of throttling shared by the HTTP middleware and by paths the
// middleware cannot see (operations over a WebSocket, per-user quotas).
type Limiter struct {
	limiter *redis_rate.Limiter
	limit   redis_rate.Limit
}

// NewLimiter builds a limiter allowing rate events per period (burst = rate).
func NewLimiter(rdb *redis.Client, rate int, period time.Duration) *Limiter {
	return &Limiter{
		limiter: redis_rate.NewLimiter(rdb),
		limit:   redis_rate.Limit{Rate: rate, Period: period, Burst: rate},
	}
}

// Allow spends one event from key's budget. It fails open on limiter errors
// (Redis down) — availability over throttling — and says so in the log:
// silently dropping the error made "rate limiting is off" indistinguishable
// from "nobody hit a limit". One line per event while Redis is down is the
// intended volume — an unprotected surface should be noisy.
func (l *Limiter) Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration) {
	res, err := l.limiter.Allow(ctx, key, l.limit)
	if err != nil {
		logger.From(ctx).Warn("rate limiter unavailable, allowing request", "error", err)
		return true, 0
	}
	return res.Allowed > 0, res.RetryAfter
}

// RateLimit returns a Redis-backed (GCRA) rate-limiting middleware.
// Mirrors @fastify/rate-limit: RateLimitMax requests per RateLimitWindow.
// For /upload and /graphql the key is "auth:{ip}", otherwise the bare IP,
// matching the TypeScript keyGenerator.
func RateLimit(rdb *redis.Client) func(http.Handler) http.Handler {
	limiter := NewLimiter(rdb, config.RateLimitMax, config.RateLimitWindow)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, retry := limiter.Allow(r.Context(), RateKey(r))
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
