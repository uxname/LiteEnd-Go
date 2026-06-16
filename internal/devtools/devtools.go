// Package devtools serves developer-facing helper endpoints: a dev launcher
// page (/dev) and the OpenAPI spec + Swagger UI (/swagger).
package devtools

import (
	_ "embed"
	"fmt"
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
// Light "engineering blueprint" theme: paper grid, soft glow, white cards
// with a per-tool accent colour driven by the --c custom property.
const devLauncherCSS = `:root{color-scheme:light}
*{box-sizing:border-box}
body{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
margin:0;min-height:100vh;color:#0f172a;
background-color:#f5f6f8;
background-image:linear-gradient(rgba(15,23,42,.045) 1px,transparent 1px),
linear-gradient(90deg,rgba(15,23,42,.045) 1px,transparent 1px);
background-size:30px 30px;-webkit-font-smoothing:antialiased}
body::before{content:"";position:fixed;inset:0;pointer-events:none;z-index:0;
background:radial-gradient(820px 420px at 12% -6%,rgba(99,102,241,.12),transparent 60%),
radial-gradient(720px 420px at 100% 2%,rgba(8,145,178,.10),transparent 55%),
radial-gradient(680px 520px at 60% 118%,rgba(16,185,129,.08),transparent 60%)}
.wrap{position:relative;z-index:1;max-width:1080px;margin:0 auto;
padding:clamp(2.5rem,6vw,5rem) clamp(1.25rem,4vw,3rem)}
header{margin-bottom:2.75rem;max-width:62ch}
.eyebrow{display:inline-flex;align-items:center;gap:.55rem;
font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.74rem;font-weight:600;
letter-spacing:.14em;text-transform:uppercase;color:#475569;margin-bottom:1.15rem}
.eyebrow .dot{width:.55rem;height:.55rem;border-radius:50%;background:#10b981;
box-shadow:0 0 0 0 rgba(16,185,129,.5);animation:pulse 2.2s infinite}
@keyframes pulse{0%{box-shadow:0 0 0 0 rgba(16,185,129,.45)}70%{box-shadow:0 0 0 9px rgba(16,185,129,0)}100%{box-shadow:0 0 0 0 rgba(16,185,129,0)}}
h1{font-size:clamp(2.1rem,5vw,3.4rem);font-weight:800;margin:0 0 .6rem;
letter-spacing:-.03em;line-height:1.04;color:#0f172a}
h1 em{font-style:normal;background:linear-gradient(100deg,#6366f1,#0891b2 70%);
-webkit-background-clip:text;background-clip:text;color:transparent}
p.sub{color:#64748b;margin:0;font-size:1.1rem;line-height:1.55}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:1.25rem}
a.tool{--c:#6366f1;position:relative;display:block;background:#fff;
border:1px solid rgba(15,23,42,.08);border-radius:18px;
padding:1.4rem 1.5rem 1.3rem;text-decoration:none;color:inherit;overflow:hidden;
box-shadow:0 1px 2px rgba(15,23,42,.04),0 10px 28px -20px rgba(15,23,42,.25);
transition:transform .2s ease,box-shadow .2s ease,border-color .2s ease}
a.tool::before{content:"";position:absolute;left:0;top:0;bottom:0;width:4px;
background:var(--c);opacity:.9;transition:width .2s ease}
a.tool:hover{transform:translateY(-5px);
border-color:color-mix(in srgb,var(--c) 45%,transparent);
box-shadow:0 1px 2px rgba(15,23,42,.04),0 22px 44px -22px color-mix(in srgb,var(--c) 60%,transparent)}
a.tool:hover::before{width:6px}
.row{display:flex;align-items:center;gap:.9rem;margin-bottom:.95rem}
.ico{flex:0 0 auto;width:3rem;height:3rem;display:grid;place-items:center;font-size:1.45rem;
border-radius:14px;background:color-mix(in srgb,var(--c) 12%,#fff);color:var(--c);
border:1px solid color-mix(in srgb,var(--c) 24%,transparent)}
.num{margin-left:auto;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;
font-size:.78rem;font-weight:600;color:#cbd5e1;letter-spacing:.05em}
.t{font-weight:650;font-size:1.08rem;letter-spacing:-.01em;color:#0f172a}
.d{color:#64748b;font-size:.875rem;margin-top:.35rem;line-height:1.5}
.go{margin-top:1.05rem;display:inline-flex;align-items:center;gap:.35rem;
font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.76rem;font-weight:600;
color:var(--c)}
.go .arrow{transition:transform .2s ease}
a.tool:hover .go .arrow{transform:translate(3px,-3px)}
footer{margin-top:3rem;padding-top:1.5rem;border-top:1px solid rgba(15,23,42,.08);
color:#94a3b8;font-size:.8rem;font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
footer code{color:#475569;background:rgba(15,23,42,.05);padding:.15rem .45rem;border-radius:6px}`

// DevLauncher renders a self-contained HTML control-room page linking to all
// dev tools and dashboards.
func DevLauncher(links []Link) http.HandlerFunc {
	// Per-card accent colours so the launcher reads as a colourful board
	// rather than a uniform list. Cycled by card position.
	accents := []string{"#6366f1", "#0891b2", "#059669", "#d97706", "#e11d48", "#7c3aed"}

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>LiteEnd-Go · Dev Ops Control Room</title><style>` + devLauncherCSS + `</style></head>` +
		`<body><div class="wrap"><header>` +
		`<span class="eyebrow"><span class="dot"></span>liteend-go · all systems</span>` +
		`<h1>Dev Ops <em>Control Room</em></h1>` +
		`<p class="sub">Unified access to every tool, dashboard and API surface in your local stack.</p>` +
		`</header><div class="grid">`)
	for i, l := range links {
		icon := l.Icon
		if icon == "" {
			icon = "→"
		}
		accent := accents[i%len(accents)]
		b.WriteString(`<a class="tool" style="--c:` + accent + `" href="` + html.EscapeString(l.URL) +
			`" target="_blank" rel="noopener">` +
			`<div class="row"><span class="ico">` + html.EscapeString(icon) + `</span>` +
			`<span class="num">` + fmt.Sprintf("%02d", i+1) + `</span></div>` +
			`<div class="t">` + html.EscapeString(l.Title) + `</div>` +
			`<div class="d">` + html.EscapeString(l.Desc) + `</div>` +
			`<span class="go">open<span class="arrow">↗</span></span></a>`)
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
