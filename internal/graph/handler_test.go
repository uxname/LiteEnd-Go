package graph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/graph/resolver"
)

// originSelf asks wsHandshake to send the test server's own URL as Origin.
const originSelf = "self"

// wsHandshake dials the subscription transport of a handler built with the
// given CORS allowlist and reports the handshake status code. An empty origin
// sends no Origin header, like a non-browser client.
func wsHandshake(t *testing.T, allowedOrigins []string, origin string) int {
	t.Helper()
	mw := auth.NewMiddleware(nil, nil, true)
	srv := httptest.NewServer(NewHandler(&resolver.Resolver{}, mw, false, allowedOrigins))
	t.Cleanup(srv.Close)

	if origin == originSelf {
		origin = srv.URL
	}
	opts := &coderws.DialOptions{Subprotocols: []string{"graphql-transport-ws"}}
	if origin != "" {
		opts.HTTPHeader = http.Header{"Origin": []string{origin}}
	}
	conn, resp, err := coderws.Dial(context.Background(), strings.Replace(srv.URL, "http", "ws", 1), opts)
	if conn != nil {
		_ = conn.CloseNow()
	}
	require.NotNil(t, resp, "handshake produced no response: %v", err)
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	return resp.StatusCode
}

func TestWebsocket_EmptyAllowlistRejectsForeignOrigin(t *testing.T) {
	t.Parallel()
	// isProd=false: an empty CORS_ORIGIN must not mean allow-all outside production.
	status := wsHandshake(t, nil, "http://evil.example")
	require.Equal(t, http.StatusForbidden, status)
}

func TestWebsocket_EmptyAllowlistAcceptsOriginlessClient(t *testing.T) {
	t.Parallel()
	status := wsHandshake(t, nil, "")
	require.Equal(t, http.StatusSwitchingProtocols, status)
}

func TestWebsocket_EmptyAllowlistAcceptsSameOrigin(t *testing.T) {
	t.Parallel()
	status := wsHandshake(t, nil, originSelf)
	require.Equal(t, http.StatusSwitchingProtocols, status)
}

func TestWebsocket_ListedOriginAccepted(t *testing.T) {
	t.Parallel()
	status := wsHandshake(t, []string{"http://localhost:3000"}, "http://localhost:3000")
	require.Equal(t, http.StatusSwitchingProtocols, status)
}

func TestWebsocket_UnlistedOriginRejected(t *testing.T) {
	t.Parallel()
	status := wsHandshake(t, []string{"http://localhost:3000"}, "http://evil.example")
	require.Equal(t, http.StatusForbidden, status)
}
