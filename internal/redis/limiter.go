package redis

import (
	"context"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

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

// Limiter returns a limiter over this client (see NewLimiter).
func (c *Client) Limiter(rate int, period time.Duration) *Limiter {
	return NewLimiter(c.rdb, rate, period)
}
