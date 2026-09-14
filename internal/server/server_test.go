package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/config"
)

// logLines runs fn with a JSON logger writing into a buffer and returns the
// decoded lines, so a test can assert on what actually reached the log.
func logLines(t *testing.T, fn func(log *slog.Logger)) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	fn(slog.New(slog.NewJSONHandler(&buf, nil)))

	var out []map[string]any
	dec := json.NewDecoder(&buf)
	// Numbers stay json.Number so a status code can be compared as the integer
	// it is, not as a float.
	dec.UseNumber()
	for dec.More() {
		var line map[string]any
		require.NoError(t, dec.Decode(&line))
		out = append(out, line)
	}
	return out
}

// intField reads a numeric log field as an int.
func intField(t *testing.T, line map[string]any, key string) int {
	t.Helper()
	num, ok := line[key].(json.Number)
	require.True(t, ok, "field %q is not a number", key)
	v, err := num.Int64()
	require.NoError(t, err)
	return int(v)
}

func lineByMsg(lines []map[string]any, msg string) map[string]any {
	for _, l := range lines {
		if l["msg"] == msg {
			return l
		}
	}
	return nil
}

// A panicking request must still produce an access-log line. RequestLogger has
// no defer, so a panic unwinds straight past it: when Recoverer is registered
// OUTSIDE RequestLogger the 500 is logged as `panic_recovered` and nothing ever
// records the method/path/status. This pins the middleware order that fixes it.
func TestRouter_PanicStillLogsHTTPRequest(t *testing.T) {
	t.Parallel()

	lines := logLines(t, func(log *slog.Logger) {
		srv := New(&config.Config{Env: "test"}, log, nil)
		srv.Router().Get("/boom", func(http.ResponseWriter, *http.Request) {
			panic("boom")
		})

		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	panicLine := lineByMsg(lines, "panic_recovered")
	require.NotNil(t, panicLine, "the panic itself must be logged")
	require.Equal(t, "/boom", panicLine["path"], "the panic line names the URL that blew up")
	require.NotEmpty(t, panicLine["stack"])

	reqLine := lineByMsg(lines, "http_request")
	require.NotNil(t, reqLine, "a panicking request must not vanish from the access log")
	require.Equal(t, http.StatusInternalServerError, intField(t, reqLine, "status"))
	require.Equal(t, "/boom", reqLine["path"])
	require.Equal(t, slog.LevelError.String(), reqLine["level"], "a 5xx is an ERROR line")
	require.Equal(t, panicLine["request_id"], reqLine["request_id"], "both lines share one id")
}

// The catch-all 404 is a client fault, not ours: WARN, not ERROR.
func TestRouter_NotFoundLogsAtWarn(t *testing.T) {
	t.Parallel()

	lines := logLines(t, func(log *slog.Logger) {
		srv := New(&config.Config{Env: "test"}, log, nil)
		// chi short-circuits to NotFound *before* the middleware chain when the
		// mux has no routes at all, so register one to exercise the real path.
		srv.Router().Get("/", func(http.ResponseWriter, *http.Request) {})
		srv.Router().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/nope", nil))
	})

	reqLine := lineByMsg(lines, "http_request")
	require.NotNil(t, reqLine)
	require.Equal(t, slog.LevelWarn.String(), reqLine["level"])
	require.Equal(t, http.StatusNotFound, intField(t, reqLine, "status"))
}

func TestRouter_PropagatesRequestIDHeader(t *testing.T) {
	t.Parallel()

	srv := New(&config.Config{Env: "test", CORSOrigin: []string{"http://localhost:3000"}}, slog.Default(), nil)
	srv.Router().Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("generates request id if missing", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotEmpty(t, rec.Header().Get("X-Request-Id"))
	})

	t.Run("preserves incoming request id", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Header.Set("X-Request-Id", "trace-client-id-777")
		srv.Router().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "trace-client-id-777", rec.Header().Get("X-Request-Id"))
	})
}
