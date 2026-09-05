// Package health implements the two probes an orchestrator asks for: liveness
// ("is this process running, or should it be restarted?") and readiness ("can
// this replica take traffic right now?").
//
// They are separate on purpose. A liveness probe that pinged the database would
// fail on every replica the moment the database blinked, and every orchestrator
// answers a failed liveness probe by killing the container — one blip would
// restart the whole fleet instead of briefly draining traffic. So liveness
// touches nothing external, and only readiness looks at the dependencies.
package health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime/metrics"

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
}

// New builds a Checker over the given dependencies.
func New(db, redis Pinger) *Checker {
	return &Checker{db: db, redis: redis}
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
// replica serves traffic with (database, Redis, its own heap) are usable, and
// answers 503 when one is not, so a proxy stops routing to this replica.
func (c *Checker) Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), config.HealthCheckTimeout)
		defer cancel()

		// A few quick checks, bounded by the context timeout above. Sequential is
		// fine here — there's no benefit in adding goroutines for three fast probes.
		checks := map[string]checkResult{
			"database": ping(ctx, c.db),
			"redis":    ping(ctx, c.redis),
			"memory":   memoryCheck(),
		}

		ok := true
		for _, res := range checks {
			if res.Status != statusOK {
				ok = false
				break
			}
		}

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
		// Log the real cause server-side; expose only a generic status to the
		// unauthenticated readiness endpoint so raw driver/connection details
		// (which can include credentials) never leak.
		logger.From(ctx).Warn("health dependency unavailable", "error", err)
		return checkResult{Status: statusError, Error: "unavailable"}
	}
	return checkResult{Status: statusOK}
}

func memoryCheck() checkResult {
	// runtime/metrics reads counters the runtime already maintains; unlike
	// runtime.ReadMemStats it never stops the world, which matters on a public
	// endpoint a load balancer polls every few seconds.
	sample := []metrics.Sample{{Name: heapMetric}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		// A Go upgrade dropped the metric: not a reason to fail the probe (and
		// reading the value of an unsupported metric panics). TestMemoryCheck
		// fails loudly instead.
		return checkResult{Status: statusOK}
	}
	heapMB := sample[0].Value.Uint64() / (1024 * 1024)
	if heapMB > config.HeapThresholdMB {
		return checkResult{Status: statusError, Error: "heap usage above threshold"}
	}
	return checkResult{Status: statusOK}
}
