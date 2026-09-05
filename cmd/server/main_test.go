package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// C9: the image's HEALTHCHECK runs `server -healthcheck`, and a failed
// HEALTHCHECK is what makes an orchestrator restart the container. The probe
// must therefore hit liveness: pointed at readiness — the probe that reports
// the dependencies — a single database blip would fail the healthcheck on every
// replica at once and restart the whole fleet instead of draining traffic from
// it.
func TestC9_HealthcheckProbesLiveness(t *testing.T) {
	probed := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed <- r.URL.Path
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	// healthcheck() probes 127.0.0.1:$PORT — exactly where httptest listens.
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	t.Setenv("PORT", port)

	require.Equal(t, 0, healthcheck())
	require.Equal(t, "/livez", <-probed,
		"the container healthcheck must probe liveness, not a dependency-checking endpoint")
}
