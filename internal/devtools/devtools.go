// Package devtools serves developer-facing helper endpoints: a dev launcher
// page (/dev) and the OpenAPI spec + Swagger UI (/swagger).
package devtools

import (
	_ "embed"
	"net/http"
	"strings"
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
}

// DevLauncher renders an HTML page linking to all dev tools and dashboards,
// mirroring the TypeScript dev-launcher.
func DevLauncher(links []Link) http.HandlerFunc {
	const (
		head = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>LiteEnd-Go · Dev Ops Control Room</title>
<style>body{font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0;margin:0;padding:3rem}
h1{font-weight:600;margin:0 0 .25rem}p.sub{color:#94a3b8;margin:0 0 1.5rem}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:1rem;max-width:760px}
a.tool{display:block;background:#1e293b;border:1px solid #334155;border-radius:12px;padding:1rem 1.25rem;
text-decoration:none;color:#e2e8f0;transition:border-color .15s}
a.tool:hover{border-color:#38bdf8}
a.tool .t{color:#38bdf8;font-weight:600}a.tool .d{color:#94a3b8;font-size:.85rem;margin-top:.25rem}</style></head>
<body><h1>Dev Ops Control Room</h1><p class="sub">Unified infrastructure access.</p><div class="grid">`
		foot = `</div></body></html>`
	)
	var b strings.Builder
	b.WriteString(head)
	for _, l := range links {
		b.WriteString(`<a class="tool" href="` + l.URL + `" target="_blank" rel="noopener">` +
			`<div class="t">` + l.Title + `</div><div class="d">` + l.Desc + `</div></a>`)
	}
	b.WriteString(foot)
	page := b.String()

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, private")
		_, _ = w.Write([]byte(page))
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
