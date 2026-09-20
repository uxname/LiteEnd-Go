// Package devtools serves developer-facing helper endpoints: a dev launcher
// page (/dev) and the OpenAPI spec + its reference UI (/docs).
package devtools

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
)

// openapiSpec is the REST API contract, embedded from openapi.yaml so it can be
// edited as a real YAML file (IDE highlighting, validation, clean diffs) and
// guarded against route drift by a test.
//
//go:embed openapi.yaml
var openapiSpec []byte

// OpenAPISpecBytes returns the raw embedded OpenAPI document (used by tests).
func OpenAPISpecBytes() []byte { return openapiSpec }

// devCSP allows the CDN-hosted assets and inline scripts/styles that the
// GraphQL playground and the API reference need. It is applied ONLY to those
// dev-tool pages — the strict default-src 'self' policy stays in force for the
// API. Both load from jsdelivr; unpkg.com went out with the UI it served.
const devCSP = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://fonts.googleapis.com; " +
	"img-src 'self' data: https:; " +
	"font-src 'self' data: https://fonts.gstatic.com; " +
	"connect-src 'self' https://cdn.jsdelivr.net; " +
	"worker-src 'self' blob:"

// RelaxCSP overrides the strict global CSP with devCSP for dev-tool pages.
func RelaxCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", devCSP)
		next.ServeHTTP(w, r)
	})
}

// Link is one entry on the dev launcher page.
type Link struct {
	Title string
	Desc  string
	URL   string
	// Icon is a short decorative glyph (emoji or symbol) shown on the card.
	// Optional — a neutral default is used when empty.
	Icon string
}

// devLauncherHTML is the /dev page: markup and stylesheet in one real HTML file,
// so it is edited with an HTML editor instead of inside Go string literals.
// Everything is inline + system fonts so the page renders fully offline,
// independent of the CDN assets the playground/reference pages rely on.
//
//go:embed dev.html
var devLauncherHTML string

// DevLauncher renders a self-contained HTML control-room page linking to all
// dev tools and dashboards.
//
// html/template escapes each value for the place it lands in — an href is not
// only HTML-escaped but also refused when it is a "javascript:" URL, which a
// hand-rolled html.EscapeString does not do. The links are static, so the page
// is rendered once, here, and served as bytes; a template that does not parse or
// execute is a programming error in dev.html, caught by the test that renders it.
func DevLauncher(links []Link) http.HandlerFunc {
	var page bytes.Buffer
	tmpl, err := template.New("dev").Parse(devLauncherHTML)
	if err == nil {
		err = tmpl.Execute(&page, links)
	}

	return func(w http.ResponseWriter, _ *http.Request) {
		if err != nil {
			http.Error(w, "dev launcher template: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, private")
		_, _ = w.Write(page.Bytes())
	}
}

// scalarVersion pins the API reference bundle EXACTLY — not @latest, not a
// major range. The page runs third-party JavaScript behind our own basic auth,
// so a floating tag would let a compromised release walk straight in.
const scalarVersion = "1.69.2"

// scalarSRI is the subresource-integrity hash of that exact bundle, verified
// against the npm tarball of the same version. Pinning the version stops a new
// release from sneaking in; this stops the CDN from serving something else
// under the pinned one. Bumping scalarVersion without recomputing this leaves a
// blank page — which is the point, and TestScalarUI_… says how to recompute it.
const scalarSRI = "sha384-WIChsUVC1uJ+G1lFA6lPYzOUgXAe8dVxUFIZ7lcENNtV34Esuo81NIxsj8EAQeHB"

// scalarUIHTML renders the reference page. proxyUrl is blanked (the default
// routes requests through proxy.scalar.com) and withDefaultFonts is off (the
// default pulls fonts from fonts.scalar.com). The bundle also calls its own
// registry on api.scalar.com when the page opens, with no option to switch that
// off, so connect-src above is what actually stops it: these pages talk to
// jsdelivr and to us, and to nobody else.
const scalarUIHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>LiteEnd-Go API</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body><div id="app"></div>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@{{.Version}}/dist/browser/standalone.js" integrity="{{.SRI}}" crossorigin="anonymous"></script>
<script>Scalar.createApiReference('#app', {url: {{.SpecURL}}, proxyUrl: '', withDefaultFonts: false})</script>
</body></html>`

// ScalarUI serves an API reference page that loads the OpenAPI spec from
// specURL. Like the dev launcher it is rendered once through html/template, so
// specURL is escaped for the JavaScript string it lands in instead of being
// concatenated into the markup.
func ScalarUI(specURL string) http.HandlerFunc {
	var page bytes.Buffer
	tmpl, err := template.New("scalar").Parse(scalarUIHTML)
	if err == nil {
		err = tmpl.Execute(&page, struct{ SpecURL, Version, SRI string }{specURL, scalarVersion, scalarSRI})
	}

	return func(w http.ResponseWriter, _ *http.Request) {
		if err != nil {
			http.Error(w, "scalar template: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page.Bytes())
	}
}

// OpenAPISpec serves the embedded OpenAPI 3 document (YAML), which the
// reference UI loads natively. The REST surface is intentionally tiny, so the
// spec is a hand-maintained artifact (kept honest by a route-sync test) rather
// than a swaggo codegen pipeline.
func OpenAPISpec() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapiSpec)
	}
}
