package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/devtools"
	"github.com/uxname/liteend-go/internal/health"
	"github.com/uxname/liteend-go/internal/upload"
)

// devRoutes are infrastructure/tooling routes intentionally excluded from the
// REST OpenAPI spec: GraphQL has its own contract (schema + introspection), and
// the dev pages/docs are tooling, not part of the public REST surface.
var devRoutes = map[string]bool{
	"/graphql":      true, // GraphQL endpoint — documented via the GraphQL schema
	"/playground":   true, // GraphQL IDE (dev tool)
	"/dev":          true, // dev launcher page
	"/swagger":      true, // Swagger UI (renders the spec)
	"/openapi.yaml": true, // the spec document itself
	"/favicon.ico":  true, // browser noise silencer
}

// testRouteDeps builds mountRoutes dependencies with no-op handlers/middleware.
// Route *registration* never invokes the handlers, so stubs are sufficient to
// enumerate the route topology.
func testRouteDeps() routeDeps {
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	passthrough := func(next http.Handler) http.Handler { return next }
	return routeDeps{
		live:       noop,
		ready:      noop,
		graphql:    noop,
		graphqlMW:  nil,
		upload:     upload.NewHandler(nil), // svc unused during registration
		uploadAuth: passthrough,
		devAuth:    passthrough,
		devLinks:   nil,
	}
}

// normalizePath canonicalises a path so chi patterns and OpenAPI templates
// compare equal: every parameter segment (chi "{x}" or "*", OpenAPI "{x}")
// becomes "{}".
func normalizePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "*" || (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

func key(method, path string) string {
	return strings.ToUpper(method) + " " + normalizePath(path)
}

func TestOpenAPISpecIsValid(t *testing.T) {
	t.Parallel()
	// Task 3: the embedded spec must be a structurally valid OpenAPI 3 document,
	// not merely valid YAML.
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(devtools.OpenAPISpecBytes())
	require.NoError(t, err, "openapi.yaml must parse")
	require.NoError(t, doc.Validate(loader.Context), "openapi.yaml must be a valid OpenAPI 3 document")
}

func TestOpenAPISpecMatchesRoutes(t *testing.T) {
	t.Parallel()
	// Parse the spec into a (METHOD path) set.
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(devtools.OpenAPISpecBytes())
	require.NoError(t, err)

	specSet := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			specSet[key(method, path)] = true
		}
	}

	// Enumerate the app's actual routes.
	r := chi.NewRouter()
	mountRoutes(r, testRouteDeps())

	routeSet := map[string]bool{}
	err = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routeSet[key(method, route)] = true
		return nil
	})
	require.NoError(t, err)

	// 1) Every REST route (excluding dev/tooling routes) must be documented.
	for rk := range routeSet {
		path := strings.SplitN(rk, " ", 2)[1]
		if devRoutes[path] {
			continue
		}
		require.Truef(t, specSet[rk],
			"route %q exists but is not documented in openapi.yaml (add it, or allowlist if it's a dev route)", rk)
	}

	// 2) Every documented operation must correspond to a real route (catches a
	// spec entry whose route was renamed/removed, and method mismatches).
	for sk := range specSet {
		require.Truef(t, routeSet[sk],
			"openapi.yaml documents %q but no matching route is registered", sk)
	}
}

// C6: uploaded files live in the shared object store and are fetched straight
// from it, so this app must expose no download route at all. The sync test
// above only proves the router and the spec agree — they would agree just as
// happily if both still carried GET /uploads/*, which is why this check names
// the route instead of comparing the two sides.
func TestC6_NoUploadDownloadRouteExists(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	mountRoutes(r, testRouteDeps())

	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		require.NotContains(t, route, "/uploads",
			"the app must serve no uploaded files: %s %s is still registered", method, route)
		return nil
	})
	require.NoError(t, err)

	require.NotContains(t, string(devtools.OpenAPISpecBytes()), "/uploads",
		"openapi.yaml still documents a file-download route")
}

// downPinger stands in for a dependency that is unreachable — the database blip
// the liveness/readiness split exists for.
type downPinger struct{}

func (downPinger) Ping(context.Context) error { return errors.New("connection refused") }

// C9: the two probes must be wired to different handlers. mountRoutes is where
// that wiring is decided, so a real liveness handler and a readiness checker
// over dead dependencies are passed in here: /livez has to stay 200 (an
// orchestrator restarts whatever fails it — one database blip must not restart
// every replica), while /readyz reports the outage so a proxy drains the
// replica.
func TestC9_LivenessAndReadinessAreWiredApart(t *testing.T) {
	t.Parallel()
	deps := testRouteDeps()
	deps.live = health.Live()
	deps.ready = health.New(downPinger{}, downPinger{}).Ready()

	r := chi.NewRouter()
	mountRoutes(r, deps)

	for path, want := range map[string]int{
		"/livez":  http.StatusOK,
		"/readyz": http.StatusServiceUnavailable,
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equalf(t, want, rec.Code, "GET %s with database and Redis unreachable", path)
	}
}

// C9: the app has exactly two probes — /livez and /readyz — and /health is not
// one of them. The sync test above only proves the router and the spec agree,
// and they would agree just as happily if both carried GET /health, which is
// why this check names the path instead of comparing the two sides. A third
// route serving the readiness verdict is a second name for one answer: one
// more thing to keep in step, and one more chance for a proxy to gate traffic
// on the probe that restarts containers.
func TestC9_ProbeSurfaceIsLivezAndReadyzOnly(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	mountRoutes(r, testRouteDeps())

	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		require.NotEqualf(t, "/health", route, "%s %s is registered again", method, route)
		return nil
	})
	require.NoError(t, err)

	require.NotContains(t, string(devtools.OpenAPISpecBytes()), "/health",
		"openapi.yaml documents a probe route the app does not serve")
}
