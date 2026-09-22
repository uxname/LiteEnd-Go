package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/graph/resolver"
	"github.com/uxname/liteend-go/internal/logger"
)

// mockProfiles resolves every mock-auth caller to one dummy USER profile, so
// WebSocket tests can open an authenticated connection without OIDC.
type mockProfiles struct{}

func (mockProfiles) FindOrCreateBySub(_ context.Context, sub string) (sqlc.Profile, error) {
	return sqlc.Profile{ID: 7, OidcSub: sub, Roles: []sqlc.ProfileRole{sqlc.ProfileRoleUSER}}, nil
}

func (mockProfiles) FindBySub(context.Context, string) (*sqlc.Profile, error) {
	return nil, context.Canceled
}

func (mockProfiles) FindOrCreateMockUser(context.Context) (sqlc.Profile, error) {
	return sqlc.Profile{ID: 7, OidcSub: "mock", Roles: []sqlc.ProfileRole{sqlc.ProfileRoleUSER}}, nil
}

// syncBuffer is a goroutine-safe log sink: gqlgen logs from its own goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer log line: %w", err)
	}
	return n, nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// wsTestServer serves h with a request-scoped JSON logger writing to the
// returned buffer, mirroring how ContextLogger feeds logger.From in production.
func wsTestServer(t *testing.T, h http.Handler) (wsURL string, logs *syncBuffer) {
	t.Helper()
	logs = &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(logger.Into(r.Context(), log)))
	}))
	t.Cleanup(srv.Close)
	return strings.Replace(srv.URL, "http", "ws", 1), logs
}

// mockHandler is NewHandler in mock-auth mode, so connection_init authenticates.
func mockHandler(r *resolver.Resolver) http.Handler {
	return NewHandler(r, auth.NewMiddleware(nil, mockProfiles{}, true), true, nil)
}

type wsFrame struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// wsDial opens a graphql-transport-ws connection and sends connection_init
// with the given payload.
func wsDial(t *testing.T, url string, initPayload map[string]any) *coderws.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := coderws.Dial(ctx, url, &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}})
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	raw, err := json.Marshal(initPayload)
	require.NoError(t, err)
	require.NoError(t, wsjson.Write(ctx, conn, wsFrame{Type: "connection_init", Payload: raw}))
	return conn
}

// wsRead reads the next frame, failing the test after timeout.
func wsRead(t *testing.T, conn *coderws.Conn, timeout time.Duration) (wsFrame, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var f wsFrame
	if err := wsjson.Read(ctx, conn, &f); err != nil {
		return f, fmt.Errorf("read frame: %w", err)
	}
	return f, nil
}

// wsRun sends one operation and collects its frames up to and including complete.
func wsRun(t *testing.T, conn *coderws.Conn, id, query string) []wsFrame {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"query": query})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, wsjson.Write(ctx, conn, wsFrame{ID: id, Type: "subscribe", Payload: raw}))
	var frames []wsFrame
	for {
		f, err := wsRead(t, conn, 5*time.Second)
		require.NoError(t, err)
		if f.Type == "ping" || f.Type == "ka" {
			continue
		}
		frames = append(frames, f)
		if f.ID == id && f.Type == "complete" {
			return frames
		}
	}
}

// wsAck reads frames until connection_ack.
func wsAck(t *testing.T, conn *coderws.Conn) {
	t.Helper()
	for {
		f, err := wsRead(t, conn, 5*time.Second)
		require.NoError(t, err)
		if f.Type == "connection_ack" {
			return
		}
	}
}

func frameTypes(frames []wsFrame) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.Type)
	}
	return out
}

// gqlgen's WebSocket transport pulls the response handler until it returns nil
// (end of stream); the logging extension must pass that nil through instead of
// dereferencing it, or every WebSocket operation logs a recovered panic and
// sends the client a spurious error frame after its data.
func TestWebsocket_OperationCompletesWithoutPanic(t *testing.T) {
	t.Parallel()
	url, logs := wsTestServer(t, mockHandler(&resolver.Resolver{}))
	conn := wsDial(t, url, map[string]any{})
	wsAck(t, conn)

	frames := wsRun(t, conn, "1", "{ __typename }")

	require.Equal(t, []string{"next", "complete"}, frameTypes(frames))
	require.NotContains(t, logs.String(), "graphql_panic")
}

// expiringAuth authenticates every socket as one USER whose token expires at exp.
type expiringAuth struct{ exp time.Time }

func (a expiringAuth) AuthenticateCreds(context.Context, string, string) (*sqlc.Profile, time.Time) {
	return &sqlc.Profile{ID: 7, Roles: []sqlc.ProfileRole{sqlc.ProfileRoleUSER}}, a.exp
}

// wsCloseStatus waits for the server to close conn and returns the close code.
func wsCloseStatus(t *testing.T, conn *coderws.Conn, within time.Duration) coderws.StatusCode {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		_, err := wsRead(t, conn, time.Until(deadline))
		if err != nil {
			return coderws.CloseStatus(err)
		}
	}
	t.Fatalf("socket still open after %s", within)
	return -1
}

// An anonymous socket is only ever a held resource (every WS operation needs a
// user), so connection_init without valid credentials must close it with 4403.
func TestWebsocket_AnonymousInitRejected(t *testing.T) {
	t.Parallel()
	url, _ := wsTestServer(t, NewHandler(&resolver.Resolver{}, auth.NewMiddleware(nil, nil, false), true, nil))
	conn := wsDial(t, url, map[string]any{})

	require.Equal(t, coderws.StatusCode(wsCloseUnauthorized), wsCloseStatus(t, conn, 5*time.Second))
}

// Net/http drops its deadlines on hijack, so a socket that never sends
// connection_init must be closed by the transport's own timeout.
func TestWebsocket_SilentSocketClosedAfterInitTimeout(t *testing.T) {
	t.Parallel()
	h := newHandler(&resolver.Resolver{}, expiringAuth{}, true, nil,
		wsLimits{initTimeout: 200 * time.Millisecond, pingPong: time.Hour})
	url, _ := wsTestServer(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := coderws.Dial(ctx, url, &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}})
	require.NoError(t, err)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	require.Equal(t, coderws.StatusProtocolError, wsCloseStatus(t, conn, 3*time.Second))
}

// An initialised graphql-transport-ws socket that stops answering pings is
// reaped instead of being held forever.
func TestWebsocket_IdleSocketWithoutPongClosed(t *testing.T) {
	t.Parallel()
	h := newHandler(&resolver.Resolver{}, expiringAuth{}, true, nil,
		wsLimits{initTimeout: time.Second, pingPong: 100 * time.Millisecond})
	url, _ := wsTestServer(t, h)
	conn := wsDial(t, url, map[string]any{})

	_ = wsCloseStatus(t, conn, 3*time.Second) // never answers ping → must close
}

// A socket must not outlive the bearer token that authenticated it.
func TestWebsocket_SocketClosedWhenTokenExpires(t *testing.T) {
	t.Parallel()
	h := newHandler(&resolver.Resolver{}, expiringAuth{exp: time.Now().Add(300 * time.Millisecond)}, true, nil,
		wsLimits{initTimeout: time.Second, pingPong: time.Hour})
	url, _ := wsTestServer(t, h)
	conn := wsDial(t, url, map[string]any{})
	wsAck(t, conn)

	_ = wsCloseStatus(t, conn, 3*time.Second)
}
