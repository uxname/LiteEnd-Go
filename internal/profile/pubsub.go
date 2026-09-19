package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"

	goredis "github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/redis"
)

// profileChannel prefixes the Redis pub/sub channels for profile-updated events.
// Using Redis (rather than in-process channels) lets subscriptions fan out
// across multiple app instances.
const profileChannel = "profile:updated"

// channelFor names the channel that carries one profile's events. One channel
// per profile, not one for everybody: on a shared channel Redis delivered every
// event to every subscriber, and each of them decoded it just to throw it away.
func channelFor(userID int32) string {
	return profileChannel + ":" + strconv.FormatInt(int64(userID), 10)
}

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
	return ps.rdb.Publish(ctx, channelFor(p.ID), string(raw))
}

// SubscribeForUser returns a channel that emits profile updates for the given
// user id only. The channel is closed when ctx is cancelled.
func (ps *PubSub) SubscribeForUser(ctx context.Context, userID int32) <-chan sqlc.Profile {
	out := make(chan sqlc.Profile, 1)
	sub := ps.rdb.Subscribe(ctx, channelFor(userID))
	go ps.pump(ctx, sub, out, userID)
	return out
}

// pump reads Redis events and forwards the owner's updates to out until ctx is
// cancelled or the subscription closes.
func (ps *PubSub) pump(ctx context.Context, sub *goredis.PubSub, out chan<- sqlc.Profile, userID int32) {
	defer close(out)
	defer func() { _ = sub.Close() }()
	// This runs on its own goroutine, so an unrecovered panic here takes the whole
	// process down for every user — not just this subscriber. The other background
	// goroutines (HTTP, jobs, upload copy) all recover; this one has to as well.
	defer func() {
		if rec := recover(); rec != nil {
			ps.log.LogAttrs(
				ctx, slog.LevelError, "profile_pubsub_panic",
				slog.Any("panic", rec),
				slog.Int("user_id", int(userID)),
				slog.String("stack", string(debug.Stack())),
			)
		}
	}()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !ps.forward(ctx, msg.Payload, out) {
				return
			}
		}
	}
}

// forward decodes one event and sends it to out. The channel it arrived on is
// the owner's own (channelFor), so there is nothing left to filter here.
// It returns false only when ctx is cancelled (signalling pump to stop).
func (ps *PubSub) forward(ctx context.Context, payload string, out chan<- sqlc.Profile) bool {
	var p sqlc.Profile
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		ps.log.Warn("bad profile event payload", "error", err)
		return true
	}
	select {
	case out <- p:
		return true
	case <-ctx.Done():
		return false
	}
}
