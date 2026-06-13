package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	goredis "github.com/redis/go-redis/v9"

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
		return fmt.Errorf("marshal profile event: %w", err)
	}
	return ps.rdb.Publish(ctx, profileChannel, string(raw))
}

// SubscribeForUser returns a channel that emits profile updates for the given
// user id only. The channel is closed when ctx is cancelled.
func (ps *PubSub) SubscribeForUser(ctx context.Context, userID int32) <-chan sqlc.Profile {
	out := make(chan sqlc.Profile, 1)
	sub := ps.rdb.Subscribe(ctx, profileChannel)
	go ps.pump(ctx, sub, out, userID)
	return out
}

// pump reads Redis events and forwards the owner's updates to out until ctx is
// cancelled or the subscription closes.
func (ps *PubSub) pump(ctx context.Context, sub *goredis.PubSub, out chan<- sqlc.Profile, userID int32) {
	defer close(out)
	defer func() { _ = sub.Close() }()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !ps.forward(ctx, msg.Payload, out, userID) {
				return
			}
		}
	}
}

// forward decodes one event and, if it belongs to userID, sends it to out.
// It returns false only when ctx is cancelled (signalling pump to stop).
func (ps *PubSub) forward(ctx context.Context, payload string, out chan<- sqlc.Profile, userID int32) bool {
	var p sqlc.Profile
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		ps.log.Warn("bad profile event payload", "error", err)
		return true
	}
	if p.ID != userID {
		return true // filter: only the owner's updates
	}
	select {
	case out <- p:
		return true
	case <-ctx.Done():
		return false
	}
}
