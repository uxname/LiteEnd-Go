package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime/metrics"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHandler_AllUp(t *testing.T) {
	t.Parallel()
	c := New(fakePinger{}, fakePinger{})
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, statusOK, resp.Status)
	require.Equal(t, statusOK, resp.Checks["database"].Status)
	require.Equal(t, statusOK, resp.Checks["redis"].Status)
	require.Equal(t, statusOK, resp.Checks["memory"].Status)
	// The exact substring cmd/server -healthcheck greps for.
	require.Contains(t, rec.Body.String(), `"status":"ok"`)
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

func TestHandler_DBDownReturns503(t *testing.T) {
	t.Parallel()
	c := New(fakePinger{err: errors.New("connection refused")}, fakePinger{})
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

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
