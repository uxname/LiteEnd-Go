package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime/metrics"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestReady_AllUp(t *testing.T) {
	t.Parallel()
	c := New(fakePinger{}, fakePinger{})
	rec := httptest.NewRecorder()
	c.Ready().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, statusOK, resp.Status)
	require.Equal(t, statusOK, resp.Checks["database"].Status)
	require.Equal(t, statusOK, resp.Checks["redis"].Status)
	require.Equal(t, statusOK, resp.Checks["memory"].Status)
}

// TestMemoryCheck pins the metric name: an unsupported one would silently
// degrade memoryCheck into a constant "ok".
func TestMemoryCheck(t *testing.T) {
	t.Parallel()
	sample := []metrics.Sample{{Name: heapMetric}}
	metrics.Read(sample)
	require.Equal(t, metrics.KindUint64, sample[0].Value.Kind(), "%s unsupported by this Go version", heapMetric)
	require.Equal(t, statusOK, memoryCheck().Status)
}

func TestReady_DBDownReturns503(t *testing.T) {
	t.Parallel()
	c := New(fakePinger{err: errors.New("connection refused")}, fakePinger{})
	rec := httptest.NewRecorder()
	c.Ready().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, statusError, resp.Status)
	require.Equal(t, statusError, resp.Checks["database"].Status)
	require.NotEmpty(t, resp.Checks["database"].Error)
}

func TestPing_NotConfigured(t *testing.T) {
	t.Parallel()
	res := ping(context.Background(), nil)
	require.Equal(t, statusError, res.Status)
	require.Equal(t, "not configured", res.Error)
}

// C9: liveness says "the process is running" and nothing else, so it must answer
// 200 while every dependency is down — that is the whole reason it is a separate
// probe. An orchestrator restarts a container whose liveness probe fails, so a
// liveness probe that followed the database would turn one database blip into a
// restart of every replica at once, while readiness (checked below in the same
// outage) is what should go red and drain traffic instead.
func TestC9_LiveStaysOKWhileDependenciesAreDown(t *testing.T) {
	t.Parallel()
	down := fakePinger{err: errors.New("connection refused")}

	live := httptest.NewRecorder()
	Live().ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))

	require.Equal(t, http.StatusOK, live.Code, "liveness must not follow the dependencies")
	// Exact body: JSONEq pins the shape (no dependency report may creep in),
	// Contains pins the literal text cmd/server -healthcheck greps for.
	require.JSONEq(t, `{"status":"ok"}`, live.Body.String())
	require.Contains(t, live.Body.String(), `"status":"ok"`)
	require.Equal(t, "application/json", live.Header().Get("Content-Type"))

	ready := httptest.NewRecorder()
	New(down, down).Ready().ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, ready.Code,
		"readiness must report the same outage liveness ignores")
}

// C9: /readyz is what gates traffic — the reverse proxy stops routing to a
// replica that answers 503 — so the verdict must follow the dependencies and
// nothing else. A heap over the threshold is a process-local heuristic, not a
// dependency: gating on it pulls a replica that can still serve out of
// rotation, and since every replica buffers request bodies the same way, one
// load spike takes them all out at once — a total outage where a slower
// service would have done. The reading stays in the body as diagnostics, which
// is what the second half pins: reported, never judged. The image's HEALTHCHECK
// polls /livez, so nothing here ever restarts a container.
func TestC9_ReadyIgnoresHeapButFollowsDependencies(t *testing.T) {
	t.Parallel()
	hot := func() checkResult { return checkResult{Status: statusError, Error: "heap usage above threshold"} }

	up := New(fakePinger{}, fakePinger{})
	up.heap = hot
	rec := httptest.NewRecorder()
	up.Ready().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code, "a hot heap must not take a replica with live dependencies out of rotation")

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, statusOK, resp.Status)
	require.Equal(t, statusError, resp.Checks["memory"].Status, "the heap reading must stay in the body")
	require.NotEmpty(t, resp.Checks["memory"].Error, "and keep saying why")

	// Same hot heap, one dead dependency: 503, because that one is a dependency.
	down := New(fakePinger{err: errors.New("connection refused")}, fakePinger{})
	down.heap = hot
	rec = httptest.NewRecorder()
	down.Ready().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "a dead dependency must still drain traffic")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, statusError, resp.Status)
	require.Equal(t, statusError, resp.Checks["database"].Status)
}

// The X-Request-Id header is not these handlers' job: the router's middleware
// sets it for every route, and internal/server's
// TestRouter_PropagatesRequestIDHeader is where that is tested.

func TestPing_ContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := ping(ctx, fakePinger{err: context.Canceled})
	require.Equal(t, statusError, res.Status)
	require.Equal(t, "canceled", res.Error)
}

// countingPinger counts pings.
type countingPinger struct{ n atomic.Int32 }

func (p *countingPinger) Ping(context.Context) error { p.n.Add(1); return nil }

// /readyz is public and unauthenticated; each call used to ping Postgres and
// Redis. The verdict is now reused for a second, so a flood of probes costs
// the dependencies at most one ping per second each.
func TestReady_ReusesTheVerdictBriefly(t *testing.T) {
	t.Parallel()
	db, rdb := &countingPinger{}, &countingPinger{}
	c := New(db, rdb)
	c.cacheFor = 50 * time.Millisecond

	for range 5 {
		c.Ready()(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	}
	require.Equal(t, int32(1), db.n.Load())
	require.Equal(t, int32(1), rdb.n.Load())

	time.Sleep(60 * time.Millisecond)
	c.Ready()(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, int32(2), db.n.Load(), "a stale verdict is re-checked")
}
