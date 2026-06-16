// Package devtools serves developer-facing helper endpoints: a dev launcher
// page (/dev) and the OpenAPI spec + Swagger UI (/swagger).
package devtools

import (
	_ "embed"
	"html"
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
	// Icon is a short decorative glyph (emoji or symbol) shown on the card.
	// Optional — a neutral default is used when empty.
	Icon string
}

// devLauncherCSS is the self-contained stylesheet for the /dev launcher.
// Everything is inline + system fonts so the page renders fully offline,
// independent of the CDN assets the playground/Swagger pages rely on.
// Minimal Go-flavoured theme: white, hairline borders, the Go gopher cyan
// (#00ADD8) as the single accent, fast cheap hover transitions.
const devLauncherCSS = `:root{color-scheme:light}
*{box-sizing:border-box}
body{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
margin:0;min-height:100vh;color:#1a1a1a;background:#fff;-webkit-font-smoothing:antialiased}
.wrap{max-width:760px;margin:0 auto;padding:clamp(3rem,9vh,6rem) 1.5rem}
header{margin-bottom:2.5rem}
.eyebrow{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.8rem;
font-weight:600;color:#00add8;margin:0 0 .65rem}
h1{font-size:clamp(1.7rem,4vw,2.2rem);font-weight:700;margin:0 0 .4rem;
letter-spacing:-.02em;color:#1a1a1a}
p.sub{color:#6b7280;margin:0;font-size:1rem;line-height:1.5}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:.75rem}
a.tool{display:flex;align-items:center;gap:.8rem;padding:.95rem 1.05rem;
border:1px solid #e5e7eb;border-radius:10px;text-decoration:none;color:inherit;
transition:border-color .12s ease,background .12s ease}
a.tool:hover{border-color:#00add8;background:#f5fcff}
.ico{flex:0 0 auto;width:1.4rem;text-align:center;font-size:1.05rem;color:#00add8}
.meta{min-width:0}
.t{font-weight:600;font-size:.95rem;color:#1a1a1a}
.d{color:#6b7280;font-size:.8rem;margin-top:.1rem;line-height:1.4}
footer{margin-top:2.5rem;color:#9ca3af;font-size:.78rem;
font-family:ui-monospace,SFMono-Regular,Menlo,monospace}`

// DevLauncher renders a self-contained HTML control-room page linking to all
// dev tools and dashboards.
func DevLauncher(links []Link) http.HandlerFunc {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>liteend-go · dev</title><style>` + devLauncherCSS + `</style></head>` +
		`<body><div class="wrap"><header>` +
		`<p class="eyebrow">liteend-go / dev</p>` +
		`<h1>Dev tools</h1>` +
		`<p class="sub">Local tools, dashboards and API surfaces.</p>` +
		`</header><div class="grid">`)
	for _, l := range links {
		icon := l.Icon
		if icon == "" {
			icon = "→"
		}
		b.WriteString(`<a class="tool" href="` + html.EscapeString(l.URL) +
			`" target="_blank" rel="noopener">` +
			`<span class="ico">` + html.EscapeString(icon) + `</span>` +
			`<span class="meta"><span class="t">` + html.EscapeString(l.Title) + `</span>` +
			`<span class="d">` + html.EscapeString(l.Desc) + `</span></span></a>`)
	}
	b.WriteString(`</div><footer>liteend-go · dev-only · basic auth</footer>` +
		`</div></body></html>`)
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
