// Package health implements the GET /health endpoint. It returns
// {"status":"ok",...} on success to preserve the contract checked by the
// container healthcheck.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"github.com/uxname/liteend-go/internal/config"
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
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		checks := map[string]checkResult{
			"database": ping(ctx, c.db),
			"redis":    ping(ctx, c.redis),
			"memory":   memoryCheck(),
		}

		ok := true
		for _, res := range checks {
			if res.Status != "ok" {
				ok = false
				break
			}
		}

		resp := response{Status: "ok", Checks: checks}
		code := http.StatusOK
		if !ok {
			resp.Status = "error"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func ping(ctx context.Context, p Pinger) checkResult {
	if p == nil {
		return checkResult{Status: "error", Error: "not configured"}
	}
	if err := p.Ping(ctx); err != nil {
		return checkResult{Status: "error", Error: err.Error()}
	}
	return checkResult{Status: "ok"}
}

func memoryCheck() checkResult {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	heapMB := m.HeapAlloc / (1024 * 1024)
	if heapMB > config.HeapThresholdMB {
		return checkResult{Status: "error", Error: "heap usage above threshold"}
	}
	return checkResult{Status: "ok"}
}
