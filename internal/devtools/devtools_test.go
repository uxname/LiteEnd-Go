package devtools

import (
	"html"
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

// The page is rendered by html/template, so a value is escaped for the place it
// lands in: markup in a title stays text, and a link that would run script is
// replaced rather than emitted. A card with no icon falls back to the arrow.
func TestDevLauncher_EscapesValuesAndRefusesScriptLinks(t *testing.T) {
	t.Parallel()
	links := []Link{
		{Title: "<b>bold</b>", Desc: "a & b", URL: "javascript:alert(1)"},
		{Title: "Iconed", Desc: "has its own", URL: "/ok", Icon: "◈"},
	}
	rec := httptest.NewRecorder()
	DevLauncher(links).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dev", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "&lt;b&gt;bold&lt;/b&gt;")
	require.NotContains(t, body, "<b>bold</b>")
	require.Contains(t, body, "a &amp; b")
	require.NotContains(t, body, "javascript:alert", "a script URL must never reach an href")
	require.Contains(t, body, `<span class="ico">→</span>`, "no icon falls back to the arrow")
	require.Contains(t, body, `<span class="ico">◈</span>`)
	require.Contains(t, body, `href="/ok"`)
}

func TestScalarUI_EmbedsSpecURLAndPinnedBundle(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	ScalarUI("/openapi.yaml").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html"))
	body := rec.Body.String()
	require.Contains(t, body, "/openapi.yaml", "the page must point the reference at our own spec")
	require.Contains(t, body,
		"https://cdn.jsdelivr.net/npm/@scalar/api-reference@"+scalarVersion+"/dist/browser/standalone.js",
		"the bundle URL must carry the exact pinned version, never a floating tag")
	require.NotContains(t, body, "api-reference@latest", "a floating tag would let a compromised release in")
	// html/template escapes "+" in an attribute value as &#43;, so compare the
	// unescaped page — that is what the browser's parser sees.
	require.Contains(t, html.UnescapeString(body), `integrity="`+scalarSRI+`"`,
		"pinning the version is not enough on its own: without the integrity hash the CDN "+
			"can serve anything under that version. Recompute after a bump with: "+
			"curl -sL https://cdn.jsdelivr.net/npm/@scalar/api-reference@<version>/dist/browser/standalone.js "+
			"| openssl dgst -sha384 -binary | openssl base64 -A")
	require.Contains(t, body, `crossorigin="anonymous"`, "integrity is ignored without it on a cross-origin script")
	require.Contains(t, body, "proxyUrl: ''", "the default proxy.scalar.com must stay switched off")
	require.Contains(t, body, "withDefaultFonts: false", "the default fonts.scalar.com is not allowed by font-src")
}

// The reference bundle calls api.scalar.com on load and offers no switch for
// it, so connect-src is the only thing keeping these pages from talking to
// third parties. A blanket "https:" would quietly allow it again, which is why
// the directive is asserted by name rather than only against devCSP.
func TestDevCSP_ConnectSrcIsNotOpenToEveryHTTPSHost(t *testing.T) {
	t.Parallel()
	require.Contains(t, devCSP, "connect-src 'self' https://cdn.jsdelivr.net")
	require.NotContains(t, devCSP, "connect-src 'self' https:;",
		"a blanket https: lets the dev pages reach any host, telemetry included")
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
