package profile

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/redis"
)

// profileChannel is the Redis pub/sub channel for profile-updated events.
// Using Redis (rather than in-process channels) lets subscriptions fan out
// across multiple app instances.
const profileChannel = "profile:updated"

// PubSub publishes and subscribes to profile-updated events over Redis.
type PubSub struct {
	rdb *redis.Client
	log *slog.Logger
}

// NewPubSub builds a profile PubSub.
func NewPubSub(rdb *redis.Client, log *slog.Logger) *PubSub {
	return &PubSub{rdb: rdb, log: log}
}

// Publish broadcasts a profile-updated event.
func (ps *PubSub) Publish(ctx context.Context, p sqlc.Profile) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return ps.rdb.Publish(ctx, profileChannel, string(raw))
}

// SubscribeForUser returns a channel that emits profile updates for the given
// user id only. The channel is closed when ctx is cancelled.
func (ps *PubSub) SubscribeForUser(ctx context.Context, userID int32) <-chan sqlc.Profile {
	out := make(chan sqlc.Profile, 1)
	sub := ps.rdb.Subscribe(ctx, profileChannel)
	ch := sub.Channel()

	go func() {
		defer close(out)
		defer func() { _ = sub.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var p sqlc.Profile
				if err := json.Unmarshal([]byte(msg.Payload), &p); err != nil {
					ps.log.Warn("bad profile event payload", "error", err)
					continue
				}
				if p.ID != userID {
					continue // filter: only the owner's updates
				}
				select {
				case out <- p:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out
}
