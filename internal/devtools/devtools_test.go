package devtools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAPISpec_ServesYAML(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)

	OpenAPISpec().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/yaml", rec.Header().Get("Content-Type"))
	require.Equal(t, OpenAPISpecBytes(), rec.Body.Bytes())
	require.Contains(t, rec.Body.String(), "openapi:")
}

func TestDevLauncher_RendersLinks(t *testing.T) {
	t.Parallel()
	links := []Link{{Title: "PG", Desc: "browse db", URL: "http://localhost:5100"}}
	rec := httptest.NewRecorder()
	DevLauncher(links).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dev", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html"))
	body := rec.Body.String()
	require.Contains(t, body, "http://localhost:5100")
	require.Contains(t, body, "PG")
	require.Contains(t, body, "browse db")
}

func TestSwaggerUI_EmbedsSpecURL(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	SwaggerUI("/openapi.yaml").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/swagger", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `url:"/openapi.yaml"`)
}

func TestRelaxCSP_SetsDevPolicy(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })

	RelaxCSP(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dev", nil))

	require.True(t, called)
	require.Equal(t, devCSP, rec.Header().Get("Content-Security-Policy"))
}
