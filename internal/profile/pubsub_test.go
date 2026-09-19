package profile

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

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

func TestForward(t *testing.T) {
	t.Parallel()
	// The payload is what Publish puts on the wire: a marshalled sqlc.Profile.
	event, err := json.Marshal(sqlc.Profile{ID: 7, OidcSub: "sub-7"})
	require.NoError(t, err)

	// Each subtest gets its own PubSub, so the parallel ones share no log buffer.
	newPubSub := func() (*PubSub, *bytes.Buffer) {
		var logs bytes.Buffer
		return &PubSub{log: slog.New(slog.NewJSONHandler(&logs, nil))}, &logs
	}

	t.Run("delivers the decoded profile", func(t *testing.T) {
		t.Parallel()
		ps, _ := newPubSub()
		out := make(chan sqlc.Profile, 1)
		require.True(t, ps.forward(context.Background(), string(event), out))
		got := <-out
		require.Equal(t, int32(7), got.ID)
		require.Equal(t, "sub-7", got.OidcSub)
	})

	t.Run("skips a bad payload and keeps the subscription alive", func(t *testing.T) {
		t.Parallel()
		ps, logs := newPubSub()
		out := make(chan sqlc.Profile, 1)
		require.True(t, ps.forward(context.Background(), `not json`, out))
		require.Empty(t, out)
		require.Contains(t, logs.String(), "bad profile event payload")
	})

	t.Run("stops when the subscriber is gone", func(t *testing.T) {
		t.Parallel()
		ps, _ := newPubSub()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		out := make(chan sqlc.Profile) // unbuffered and unread: only ctx can unblock
		require.False(t, ps.forward(ctx, string(event), out))
	})
}
