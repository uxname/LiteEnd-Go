package profile

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// Publisher and subscriber meet on a channel named after the profile id, so an
// event reaches its owner's subscriptions and nobody else's.
func TestChannelFor_IsPerUser(t *testing.T) {
	t.Parallel()
	require.Equal(t, "profile:updated:7", channelFor(7))
	require.NotEqual(t, channelFor(7), channelFor(8))
}

// One Redis subscription per process fans events out in-process: an event on
// a profile's channel reaches that owner's local subscribers and nobody else's.
func TestDispatch_ReachesOnlyTheOwner(t *testing.T) {
	t.Parallel()
	ps := NewPubSub(nil, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	own1 := ps.SubscribeForUser(ctx, 7)
	own2 := ps.SubscribeForUser(ctx, 7)
	other := ps.SubscribeForUser(ctx, 8)
	event, err := json.Marshal(sqlc.Profile{ID: 7, OidcSub: "sub-7"})
	require.NoError(t, err)

	ps.dispatch(channelFor(7), string(event))

	require.Equal(t, int32(7), (<-own1).ID)
	require.Equal(t, int32(7), (<-own2).ID)
	require.Empty(t, other)
}

func TestDispatch_IgnoresForeignChannelsAndBadPayloads(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ps := NewPubSub(nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := ps.SubscribeForUser(ctx, 7)

	ps.dispatch("profile:updated:not-a-number", `{}`)
	ps.dispatch(channelFor(7), `not json`)

	require.Empty(t, out)
	require.Contains(t, logs.String(), "bad profile event payload")
}

// A subscriber that leaves is unregistered and its channel closed, so the
// dispatcher neither leaks it nor blocks on it.
func TestSubscribeForUser_UnregistersOnCancel(t *testing.T) {
	t.Parallel()
	ps := NewPubSub(nil, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	out := ps.SubscribeForUser(ctx, 7)

	cancel()
	_, open := <-out

	require.False(t, open)
	require.Eventually(t, func() bool { return ps.listeners(7) == 0 }, time.Second, 5*time.Millisecond)
}
