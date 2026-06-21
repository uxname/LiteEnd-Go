package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoverer_PanicReturns500(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)
	h := Recoverer(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "Internal Server Error")
}

func TestBodyLimit_RejectsOversizedBody(t *testing.T) {
	t.Parallel()
	var readErr error
	h := BodyLimit(8)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 100)))
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Error(t, readErr, "reading past the limit must fail")
}

func TestSecureHeaders_SetsHardeningHeaders(t *testing.T) {
	t.Parallel()
	h := SecureHeaders(false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))
}
