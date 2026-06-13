// Package redis wraps the go-redis client with cache and pub/sub helpers.
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/config"
)

// ErrCacheMiss is returned by Get when the key is absent.
var ErrCacheMiss = errors.New("cache miss")

// Client is a thin wrapper over *redis.Client adding typed cache helpers.
type Client struct {
	rdb *redis.Client
}

// New connects to Redis and verifies connectivity.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:            cfg.RedisAddr(),
		Password:        cfg.RedisPassword,
		DialTimeout:     config.RedisConnectTimeout,
		MaxRetryBackoff: config.RedisRetryMaxDelay,
		MinRetryBackoff: config.RedisRetryBaseDelay,
	})

	pingCtx, cancel := context.WithTimeout(ctx, config.RedisConnectTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &Client{rdb: rdb}, nil
}

// Raw exposes the underlying client (e.g. for asynq or pub/sub).
func (c *Client) Raw() *redis.Client { return c.rdb }

// Close shuts down the client.
func (c *Client) Close() error { return c.rdb.Close() }

// Ping checks connectivity (used by the health endpoint).
func (c *Client) Ping(ctx context.Context) error { return c.rdb.Ping(ctx).Err() }

// GetString returns the cached string for key, or ErrCacheMiss if absent.
func (c *Client) GetString(ctx context.Context, key string) (string, error) {
	v, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrCacheMiss
	}
	if err != nil {
		return "", fmt.Errorf("redis get %q: %w", key, err)
	}
	return v, nil
}

// SetString stores a string with a TTL.
func (c *Client) SetString(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := c.rdb.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}
	return nil
}

// Delete removes one or more keys.
func (c *Client) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}

// Publish sends a message to a pub/sub channel.
func (c *Client) Publish(ctx context.Context, channel, payload string) error {
	if err := c.rdb.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("redis publish %q: %w", channel, err)
	}
	return nil
}

// Subscribe returns a pub/sub subscription for the given channel.
func (c *Client) Subscribe(ctx context.Context, channel string) *redis.PubSub {
	return c.rdb.Subscribe(ctx, channel)
}
