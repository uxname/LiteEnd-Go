// Package devtools serves developer-facing helper endpoints: a dev launcher
// page (/dev) and the OpenAPI spec + Swagger UI (/swagger).
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
// GraphQL playground and Swagger UI need. It is applied ONLY to those dev-tool
// pages — the strict default-src 'self' policy stays in force for the API.
const devCSP = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://unpkg.com; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://unpkg.com https://fonts.googleapis.com; " +
	"img-src 'self' data: https:; " +
	"font-src 'self' data: https://fonts.gstatic.com; " +
	"connect-src 'self' https:; " +
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
// independent of the CDN assets the playground/Swagger pages rely on.
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

// SwaggerUI serves a Swagger UI page that loads the OpenAPI spec from specURL.
func SwaggerUI(specURL string) http.HandlerFunc {
	page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>LiteEnd-Go API</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>window.onload=()=>{SwaggerUIBundle({url:"` + specURL + `",dom_id:"#swagger-ui"})}</script>
</body></html>`
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}
}

// OpenAPISpec serves the embedded OpenAPI 3 document (YAML). Swagger UI loads
// YAML natively. The REST surface is intentionally tiny, so the spec is a
// hand-maintained artifact (kept honest by a route-sync test) rather than a
// swaggo codegen pipeline.
func OpenAPISpec() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapiSpec)
	}
}
