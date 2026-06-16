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
const devLauncherCSS = `:root{color-scheme:dark}
*{box-sizing:border-box}
body{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
margin:0;min-height:100vh;color:#e2e8f0;
background:#070b16;
background-image:radial-gradient(900px 600px at 12% -8%,rgba(56,189,248,.16),transparent 60%),
radial-gradient(800px 600px at 100% 0%,rgba(168,85,247,.14),transparent 55%),
radial-gradient(700px 700px at 50% 120%,rgba(34,197,94,.10),transparent 60%);
background-attachment:fixed;-webkit-font-smoothing:antialiased}
.wrap{max-width:1040px;margin:0 auto;padding:clamp(2rem,5vw,4.5rem) clamp(1.25rem,4vw,3rem)}
header{margin-bottom:2.5rem}
.badge{display:inline-flex;align-items:center;gap:.5rem;font-size:.75rem;font-weight:600;
letter-spacing:.08em;text-transform:uppercase;color:#7dd3fc;
background:rgba(56,189,248,.08);border:1px solid rgba(56,189,248,.25);
padding:.35rem .75rem;border-radius:999px;margin-bottom:1.25rem}
.badge .dot{width:.5rem;height:.5rem;border-radius:50%;background:#22c55e;
box-shadow:0 0 0 0 rgba(34,197,94,.6);animation:pulse 2s infinite}
@keyframes pulse{0%{box-shadow:0 0 0 0 rgba(34,197,94,.5)}70%{box-shadow:0 0 0 8px rgba(34,197,94,0)}100%{box-shadow:0 0 0 0 rgba(34,197,94,0)}}
h1{font-size:clamp(1.9rem,4vw,2.9rem);font-weight:700;margin:0 0 .5rem;letter-spacing:-.02em;
background:linear-gradient(120deg,#f8fafc 0%,#7dd3fc 55%,#c084fc 100%);
-webkit-background-clip:text;background-clip:text;color:transparent}
p.sub{color:#94a3b8;margin:0;font-size:1.05rem;max-width:46ch}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:1.1rem}
a.tool{position:relative;display:flex;gap:1rem;align-items:flex-start;
background:rgba(255,255,255,.035);border:1px solid rgba(255,255,255,.08);
border-radius:16px;padding:1.35rem 1.4rem;text-decoration:none;color:inherit;overflow:hidden;
backdrop-filter:blur(10px);transition:transform .2s ease,border-color .2s ease,background .2s ease}
a.tool::before{content:"";position:absolute;inset:0;border-radius:inherit;padding:1px;
background:linear-gradient(135deg,rgba(56,189,248,.5),rgba(192,132,252,.35) 50%,transparent 70%);
-webkit-mask:linear-gradient(#000 0 0) content-box,linear-gradient(#000 0 0);
-webkit-mask-composite:xor;mask-composite:exclude;opacity:0;transition:opacity .2s ease;pointer-events:none}
a.tool:hover{transform:translateY(-4px);border-color:transparent;background:rgba(255,255,255,.06)}
a.tool:hover::before{opacity:1}
.ico{flex:0 0 auto;width:2.75rem;height:2.75rem;display:grid;place-items:center;font-size:1.35rem;
border-radius:12px;background:linear-gradient(135deg,rgba(56,189,248,.18),rgba(168,85,247,.18));
border:1px solid rgba(255,255,255,.1);color:#e0f2fe}
.meta{min-width:0}
.t{font-weight:600;font-size:1.02rem;color:#f1f5f9;display:flex;align-items:center;gap:.4rem}
.t .arrow{opacity:0;transform:translateX(-4px);transition:.2s;color:#7dd3fc}
a.tool:hover .t .arrow{opacity:1;transform:translateX(0)}
.d{color:#94a3b8;font-size:.875rem;margin-top:.3rem;line-height:1.45}
footer{margin-top:2.75rem;color:#475569;font-size:.8rem;display:flex;align-items:center;gap:.5rem}
footer code{color:#64748b;background:rgba(255,255,255,.04);padding:.15rem .45rem;border-radius:6px}`

// DevLauncher renders a self-contained HTML control-room page linking to all
// dev tools and dashboards.
func DevLauncher(links []Link) http.HandlerFunc {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>LiteEnd-Go · Dev Ops Control Room</title><style>` + devLauncherCSS + `</style></head>` +
		`<body><div class="wrap"><header>` +
		`<span class="badge"><span class="dot"></span>All systems</span>` +
		`<h1>Dev Ops Control Room</h1>` +
		`<p class="sub">Unified access to every tool, dashboard and API surface in your local stack.</p>` +
		`</header><div class="grid">`)
	for _, l := range links {
		icon := l.Icon
		if icon == "" {
			icon = "→"
		}
		b.WriteString(`<a class="tool" href="` + html.EscapeString(l.URL) + `" target="_blank" rel="noopener">` +
			`<span class="ico">` + html.EscapeString(icon) + `</span>` +
			`<span class="meta"><span class="t">` + html.EscapeString(l.Title) +
			`<span class="arrow">→</span></span>` +
			`<span class="d">` + html.EscapeString(l.Desc) + `</span></span></a>`)
	}
	b.WriteString(`</div><footer>Served by <code>liteend-go</code> · dev-only · guarded by Basic Auth</footer>` +
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
