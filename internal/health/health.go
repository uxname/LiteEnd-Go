// Package health implements the GET /health endpoint. It returns
// {"status":"ok",...} on success to preserve the contract checked by the
// container healthcheck.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/logger"
)

// Status values reported per check and overall.
const (
	statusOK    = "ok"
	statusError = "error"
)

// Pinger is anything that can report its liveness.
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

// Handler returns an http.Handler serving the health report.
func (c *Checker) Handler() http.HandlerFunc {
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
		// unauthenticated /health endpoint so raw driver/connection details
		// (which can include credentials) never leak.
		logger.From(ctx).Warn("health dependency unavailable", "error", err)
		return checkResult{Status: statusError, Error: "unavailable"}
	}
	return checkResult{Status: statusOK}
}

func memoryCheck() checkResult {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	heapMB := m.HeapAlloc / (1024 * 1024)
	if heapMB > config.HeapThresholdMB {
		return checkResult{Status: statusError, Error: "heap usage above threshold"}
	}
	return checkResult{Status: statusOK}
}
