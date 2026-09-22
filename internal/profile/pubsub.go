package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/redis"
)

// profileChannel prefixes the Redis pub/sub channels for profile-updated events.
// Using Redis (rather than in-process channels) lets subscriptions fan out
// across multiple app instances.
const profileChannel = "profile:updated"

// channelFor names the channel that carries one profile's events. One channel
// per profile, not one for everybody, so the dispatcher routes an event by its
// channel name alone and never decodes it for a profile nobody watches.
func channelFor(userID int32) string {
	return profileChannel + ":" + strconv.FormatInt(int64(userID), 10)
}

// PubSub publishes and subscribes to profile-updated events over Redis.
//
// Each process holds ONE Redis subscription (a pattern over every profile
// channel) and fans its events out to local subscribers. Opening a Redis
// subscription per GraphQL subscription gave every client a dedicated Redis
// connection on demand — enough of them exhausted the Redis that also backs the
// role cache, the rate limiter and the queue for everyone.
type PubSub struct {
	rdb *redis.Client
	log *slog.Logger

	mu   sync.Mutex
	subs map[int32]map[chan sqlc.Profile]struct{}

	stop context.CancelFunc
	done chan struct{}
}

// NewPubSub builds a profile PubSub. Call Start to begin receiving events.
func NewPubSub(rdb *redis.Client, log *slog.Logger) *PubSub {
	return &PubSub{rdb: rdb, log: log, subs: map[int32]map[chan sqlc.Profile]struct{}{}}
}

// Start opens the process's Redis subscription and dispatches its events until
// ctx ends or Stop is called.
func (ps *PubSub) Start(ctx context.Context) {
	ctx, ps.stop = context.WithCancel(ctx)
	ps.done = make(chan struct{})
	sub := ps.rdb.PSubscribe(ctx, profileChannel+":*")
	go ps.pump(ctx, sub)
}

// Stop ends the Redis subscription and waits for the dispatcher to exit.
func (ps *PubSub) Stop() {
	if ps.stop == nil {
		return
	}
	ps.stop()
	<-ps.done
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
	ps.mu.Lock()
	if ps.subs[userID] == nil {
		ps.subs[userID] = map[chan sqlc.Profile]struct{}{}
	}
	ps.subs[userID][out] = struct{}{}
	ps.mu.Unlock()

	go func() {
		<-ctx.Done()
		ps.mu.Lock()
		defer ps.mu.Unlock()
		delete(ps.subs[userID], out)
		if len(ps.subs[userID]) == 0 {
			delete(ps.subs, userID)
		}
		close(out) // under mu: dispatch never sends on a closed channel
	}()
	return out
}

// listeners reports how many local subscribers a profile has.
func (ps *PubSub) listeners(userID int32) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.subs[userID])
}

// pump reads the process's Redis subscription until ctx is cancelled.
func (ps *PubSub) pump(ctx context.Context, sub *goredis.PubSub) {
	defer close(ps.done)
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
			ps.safeDispatch(ctx, msg.Channel, msg.Payload)
		}
	}
}

// safeDispatch contains a panic to one event. This goroutine serves every
// subscriber of the process, and an unrecovered panic would take the whole
// process down — the other background goroutines (HTTP, jobs, upload copy) all
// recover too.
func (ps *PubSub) safeDispatch(ctx context.Context, channel, payload string) {
	defer func() {
		if rec := recover(); rec != nil {
			ps.log.LogAttrs(
				ctx, slog.LevelError, "profile_pubsub_panic",
				slog.Any("panic", rec),
				slog.String("channel", channel),
				slog.String("stack", string(debug.Stack())),
			)
		}
	}()
	ps.dispatch(channel, payload)
}

// dispatch decodes one event and hands it to the owner's local subscribers.
// A subscriber whose buffer is still full is skipped rather than waited for:
// the event is the profile's latest state, and one slow reader must not stall
// delivery to everybody else.
func (ps *PubSub) dispatch(channel, payload string) {
	raw, ok := strings.CutPrefix(channel, profileChannel+":")
	if !ok {
		return
	}
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return
	}
	var p sqlc.Profile
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		ps.log.Warn("bad profile event payload", "error", err)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for out := range ps.subs[int32(id)] {
		select {
		case out <- p:
		default:
		}
	}
}
