package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// C3: ClientIP extracts host without port from RemoteAddr, or returns raw address on parse failure.
func TestC3_ClientIP(t *testing.T) {
	t.Parallel()

	t.Run("strips port from RemoteAddr", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		require.Equal(t, "192.0.2.1", ClientIP(req))
	})

	t.Run("returns RemoteAddr if host-port parsing fails", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "invalid-address"
		require.Equal(t, "invalid-address", ClientIP(req))
	})
}
