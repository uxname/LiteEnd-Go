package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/graph/resolver"
	"github.com/uxname/liteend-go/internal/logger"
)

// originSelf asks wsHandshake to send the test server's own URL as Origin.
const originSelf = "self"

// wsHandshake dials the subscription transport of a handler built with the
// given CORS allowlist and reports the handshake status code. An empty origin
// sends no Origin header, like a non-browser client.
func wsHandshake(t *testing.T, allowedOrigins []string, origin string) int {
	t.Helper()
	mw := auth.NewMiddleware(nil, nil, true)
	srv := httptest.NewServer(NewHandler(&resolver.Resolver{}, mw, nil, false, allowedOrigins))
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

// A resolver panic is recovered by gqlgen, never by middleware.Recoverer, so
// `panic_recovered` does not cover it. gqlgen's default prints the stack to
// stderr as raw text — unparseable and uncorrelated. This pins it to slog.
func TestRecoverPanic_LogsStructuredAndCorrelated(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	ctx := logger.Into(
		context.Background(),
		slog.New(slog.NewJSONHandler(&buf, nil)).With(slog.String("request_id", "req-9")),
	)

	userErr := recoverPanic(ctx, "boom")
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, userErr, &gqlErr)
	require.Equal(t, "internal system error", gqlErr.Message,
		"the client-facing message stays exactly gqlgen's default")

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, "graphql_panic", line["msg"])
	require.Equal(t, slog.LevelError.String(), line["level"])
	require.Equal(t, "boom", line["panic"])
	require.Equal(t, "req-9", line["request_id"])
	require.NotEmpty(t, line["stack"])
}

// postGraphQL sends one JSON GraphQL request to h and returns the response.
func postGraphQL(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func prodHandler() http.Handler {
	return NewHandler(&resolver.Resolver{}, auth.NewMiddleware(nil, nil, false), nil, true, nil)
}

// A query over the size cap is refused before parse, validation and the query
// cache, so it can neither burn CPU nor stay resident in memory.
func TestHandler_OversizedQueryRejectedBeforeParse(t *testing.T) {
	t.Parallel()
	query := "{ __typename }\n#" + strings.Repeat("x", 129<<10)

	rec := postGraphQL(t, prodHandler(), query)

	require.Contains(t, rec.Body.String(), `"QUERY_TOO_LARGE"`)
}

// Rejected queries must not be retained: before the fix every validated
// document was cached (keyed by its full text) before the complexity check.
func TestHandler_RejectedLargeQueriesAreNotRetained(t *testing.T) { //nolint:paralleltest // measures the process heap
	h := prodHandler()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range 20 {
		// valid, over-complexity, and just under the old 10 MiB body cap's reach
		query := fmt.Sprintf("{ a%d%s: __typename %s }", i, strings.Repeat("x", 1<<20), strings.Repeat("__typename ", 300))
		_ = postGraphQL(t, h, query)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(h) // a live server keeps its query cache; so must the test

	grown := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	require.Less(t, grown, int64(5<<20), "20 rejected 1 MiB queries retained %d bytes", grown)
}

// The parser token limit bounds the super-linear validation cost of small but
// dense queries (repeated fields).
func TestHandler_OverTokenQueryRejectedFast(t *testing.T) {
	t.Parallel()
	query := "{ " + strings.Repeat("__typename ", 6000) + "}"

	start := time.Now()
	rec := postGraphQL(t, prodHandler(), query)

	require.Contains(t, rec.Body.String(), "GRAPHQL_PARSE_FAILED")
	require.Less(t, time.Since(start), 200*time.Millisecond)
}

// Introspection is off in production; field suggestions must be too, or the
// schema can still be enumerated one typo at a time.
func TestHandler_NoFieldSuggestionsInProduction(t *testing.T) {
	t.Parallel()
	rec := postGraphQL(t, prodHandler(), "{ __typenam }")

	require.NotContains(t, rec.Body.String(), "Did you mean")
}

// The schema has no Upload scalar (files go through REST /upload), so the
// multipart transport only offered a CORS-simple way to send mutations.
func TestHandler_MultipartGraphQLRequestsNotAccepted(t *testing.T) {
	t.Parallel()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("operations", `{"query":"{ __typename }"}`))
	require.NoError(t, w.WriteField("map", `{}`))
	require.NoError(t, w.Close())
	req := httptest.NewRequest(http.MethodPost, "/graphql", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	prodHandler().ServeHTTP(rec, req)

	require.NotContains(t, rec.Body.String(), `"__typename"`)
}

// Validation of repeated fields is quadratic (OverlappingFieldsCanBeMerged), so
// a query under the byte and token caps could still cost seconds of CPU. The
// field count is capped on a cheap pre-parse, before validation.
func TestHandler_DenseQueryRejectedBeforeValidation(t *testing.T) {
	t.Parallel()
	query := "{ " + strings.Repeat("__typename ", 4990) + "}"

	start := time.Now()
	rec := postGraphQL(t, prodHandler(), query)

	require.Contains(t, rec.Body.String(), `"QUERY_TOO_COMPLEX"`)
	require.Less(t, time.Since(start), 100*time.Millisecond)
}
