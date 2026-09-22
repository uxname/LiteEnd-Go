// Package health implements the two probes an orchestrator asks for: liveness
// ("is this process running, or should it be restarted?") and readiness ("can
// this replica take traffic right now?").
//
// They are separate on purpose. A liveness probe that pinged the database would
// fail on every replica the moment the database blinked, and every orchestrator
// answers a failed liveness probe by killing the container — one blip would
// restart the whole fleet instead of briefly draining traffic. So liveness
// touches nothing external, and only readiness looks at the dependencies.
//
// Readiness, in turn, looks at the dependencies and nothing else. A proxy gates
// traffic on it, so anything that is not a dependency — the heap reading below
// — is reported in the body but kept out of the verdict.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
)

// Status values reported per check and overall.
const (
	statusOK    = "ok"
	statusError = "error"
)

// heapMetric is the runtime/metrics counterpart of runtime.MemStats.HeapAlloc:
// bytes held by live objects plus dead ones not yet swept.
const heapMetric = "/memory/classes/heap/objects:bytes"

// Pinger is anything that can report whether it is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Checker aggregates dependency health checks.
type Checker struct {
	db    Pinger
	redis Pinger
	// heap is the heap reading, a field only so a test can put readiness in
	// front of an over-threshold heap. The alternative — allocating 150 MB for
	// real — would also inflate the heap every parallel test in this package
	// reads, so the seam is what keeps that assertion testable at all.
	heap func() checkResult

	// The dependency verdict is reused for cacheFor: /readyz is public and
	// unauthenticated, and without this every call pinged Postgres and Redis.
	cacheFor time.Duration
	mu       sync.Mutex
	cachedAt time.Time
	cached   map[string]checkResult
}

// New builds a Checker over the given dependencies.
func New(db, redis Pinger) *Checker {
	return &Checker{db: db, redis: redis, heap: memoryCheck, cacheFor: config.ReadinessCacheTTL}
}

// dependencies returns the dependency checks, pinging at most once per
// cacheFor (callers in between wait for, then share, the fresh answer). A
// verdict cut short by a caller hanging up is not kept.
func (c *Checker) dependencies(ctx context.Context) map[string]checkResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != nil && time.Since(c.cachedAt) < c.cacheFor {
		return c.cached
	}
	// The dependency checks — every entry here gates traffic. Sequential is
	// fine: two fast probes, both bounded by the caller's context timeout.
	checks := map[string]checkResult{
		"database": ping(ctx, c.db),
		"redis":    ping(ctx, c.redis),
	}
	if ctx.Err() == nil {
		c.cached, c.cachedAt = checks, time.Now()
	}
	return checks
}

type checkResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type response struct {
	Status string                 `json:"status"`
	Checks map[string]checkResult `json:"checks"`
}

// livePayload is the liveness body, written as a fixed string rather than an
// encoded struct: answering "the process is running" must not depend on
// anything that can be unavailable. cmd/server -healthcheck greps it for
// `"status":"ok"`.
const livePayload = `{"status":"ok"}`

// Live returns the liveness handler: it reports that the process is running and
// nothing else. It is a package-level function, not a Checker method, so that
// the handler has no dependency in reach to start pinging.
func Live() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, livePayload)
	}
}

// Ready returns the readiness handler: it reports whether the dependencies this
// replica serves traffic with (database, Redis) are usable, and answers 503
// when one is not, so a proxy stops routing to this replica.
//
// Dependencies decide the verdict, and only they. The heap reading is reported
// alongside them but never judged — see the comment in the body.
func (c *Checker) Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r == nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), config.HealthCheckTimeout)
		defer cancel()

		// A copy: the heap entry below must not be written into the cached map.
		checks := maps.Clone(c.dependencies(ctx))

		ok := true
		for _, res := range checks {
			if res.Status != statusOK {
				ok = false
				break
			}
		}

		// Added after the verdict, on purpose: the heap is diagnostics, not a
		// dependency. A proxy drops a replica that answers 503 here, so gating on
		// a process-local heuristic would pull a replica that can still serve out
		// of rotation — and since every replica buffers request bodies alike, one
		// load spike would pull all of them at once. Total outage instead of a
		// slow service. Restarting a leaking replica is liveness' job, not this.
		checks["memory"] = c.heap()

		resp := response{Status: statusOK, Checks: checks}
		code := http.StatusOK
		if !ok {
			resp.Status = statusError
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func ping(ctx context.Context, p Pinger) checkResult {
	if p == nil {
		return checkResult{Status: statusError, Error: "not configured"}
	}
	if err := p.Ping(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return checkResult{Status: statusError, Error: "canceled"}
		}
		// Log the real cause server-side; expose only a generic status to the
		// unauthenticated readiness endpoint so raw driver/connection details
		// (which can include credentials) never leak.
		logger.From(ctx).Warn("health dependency unavailable", "error", err)
		return checkResult{Status: statusError, Error: "unavailable"}
	}
	return checkResult{Status: statusOK}
}

// memoryCheck reads the live heap and flags it over the threshold. Its result
// is diagnostics on the readiness body — an operator looking at why a replica
// is slow — and does not affect the readiness verdict; see Ready.
func memoryCheck() checkResult {
	// runtime/metrics reads counters the runtime already maintains; unlike
	// runtime.ReadMemStats it never stops the world, which matters on a public
	// endpoint a load balancer polls every few seconds.
	sample := []metrics.Sample{{Name: heapMetric}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		// A Go upgrade dropped the metric: reading the value of an unsupported
		// metric panics, so report "ok" and lose the diagnostic. TestMemoryCheck
		// fails loudly instead.
		return checkResult{Status: statusOK}
	}
	heapMB := sample[0].Value.Uint64() / (1024 * 1024)
	if heapMB > config.HeapThresholdMB {
		return checkResult{Status: statusError, Error: "heap usage above threshold"}
	}
	return checkResult{Status: statusOK}
}
