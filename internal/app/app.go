// Package app wires all components into a runnable application. It is used by
// the server entrypoint and by integration tests, so both exercise identical
// wiring.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db"
	"github.com/uxname/liteend-go/internal/devtools"
	"github.com/uxname/liteend-go/internal/graph"
	"github.com/uxname/liteend-go/internal/graph/resolver"
	"github.com/uxname/liteend-go/internal/health"
	appi18n "github.com/uxname/liteend-go/internal/i18n"
	appmw "github.com/uxname/liteend-go/internal/middleware"
	"github.com/uxname/liteend-go/internal/profile"
	"github.com/uxname/liteend-go/internal/queue"
	"github.com/uxname/liteend-go/internal/redis"
	"github.com/uxname/liteend-go/internal/server"
	"github.com/uxname/liteend-go/internal/upload"
)

// App bundles the assembled server with a cleanup function.
type App struct {
	Server  *server.Server
	cleanup []func()
}

// Close releases all resources (db, redis, queue worker) in reverse order.
func (a *App) Close() {
	for i := len(a.cleanup) - 1; i >= 0; i-- {
		a.cleanup[i]()
	}
}

// Build assembles the full application: migrations, db, redis, services, auth,
// GraphQL, REST upload, queue worker, and dev tools.
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	app := &App{}

	if err := db.Migrate(ctx, cfg, log); err != nil {
		return nil, err
	}

	database, err := db.New(ctx, cfg, log)
	if err != nil {
		return nil, err
	}
	app.cleanup = append(app.cleanup, database.Close)

	rdb, err := redis.New(ctx, cfg)
	if err != nil {
		app.Close()
		return nil, err
	}
	app.cleanup = append(app.cleanup, func() { _ = rdb.Close() })

	srv := server.New(cfg, log, rdb.Raw())

	// Domain services.
	profiles := profile.New(database.Queries, rdb)
	pubsub := profile.NewPubSub(rdb, log)

	// Auth.
	verifier := auth.NewVerifier(ctx, cfg)
	mockEnabled := cfg.OIDCMockEnabled && !cfg.IsProduction()
	authMW := auth.NewMiddleware(verifier, profiles, log, mockEnabled)

	// Queue.
	queueClient := queue.NewClient(rdb.Raw(), log)
	app.cleanup = append(app.cleanup, func() { _ = queueClient.Close() })
	worker := queue.NewWorker(rdb.Raw(), log)
	if err := worker.Start(); err != nil {
		app.Close()
		return nil, err
	}
	app.cleanup = append(app.cleanup, worker.Stop)

	// i18n.
	translator, err := appi18n.New(log)
	if err != nil {
		app.Close()
		return nil, err
	}

	// GraphQL.
	res := &resolver.Resolver{
		Profiles: profiles,
		PubSub:   pubsub,
		Queue:    queueClient,
		I18n:     translator,
		Log:      log,
	}
	gqlHandler := graph.NewHandler(res, authMW)
	uploadH := upload.NewHandler(upload.New(database.Queries))

	mountRoutes(srv.Router(), routeDeps{
		health:     health.New(database, rdb).Handler(),
		graphql:    gqlHandler,
		graphqlMW:  []func(http.Handler) http.Handler{translator.Middleware, authMW.Optional},
		upload:     uploadH,
		uploadAuth: authMW.RequireAuth,
		devAuth:    appmw.BasicAuth("liteend dev tools", cfg.AdminUser, cfg.AdminPassword),
		devLinks:   devLinks(cfg),
	})

	app.Server = srv
	return app, nil
}

// routeDeps carries the handlers and middleware that mountRoutes needs. It
// decouples route topology from dependency wiring, so the route set can be
// enumerated in a test (against the OpenAPI spec) without live DB/Redis.
type routeDeps struct {
	health     http.Handler
	graphql    http.Handler
	graphqlMW  []func(http.Handler) http.Handler
	upload     *upload.Handler
	uploadAuth func(http.Handler) http.Handler
	devAuth    func(http.Handler) http.Handler
	devLinks   []devtools.Link
}

// mountRoutes registers every HTTP route. This is the single source of truth
// for the app's route topology (Build and the route-sync test both use it).
func mountRoutes(r chi.Router, d routeDeps) {
	// Public REST endpoints (documented in openapi.yaml).
	r.Get("/health", d.health.ServeHTTP)
	d.upload.Register(r, d.uploadAuth) // POST /upload, GET /uploads/*

	// GraphQL (POST + WS).
	r.With(d.graphqlMW...).Handle("/graphql", d.graphql)

	// Dev tools & API docs. These pages load CDN assets + inline scripts, so they
	// run under a relaxed CSP (the strict policy still applies to the API), and
	// require Basic Auth — no anonymous access.
	r.With(d.devAuth, devtools.RelaxCSP).Get("/playground", graph.Playground("/graphql"))
	r.With(d.devAuth, devtools.RelaxCSP).Get("/dev", devtools.DevLauncher(d.devLinks))
	r.With(d.devAuth, devtools.RelaxCSP).Get("/swagger", devtools.SwaggerUI("/openapi.yaml"))
	r.With(d.devAuth).Get("/openapi.yaml", devtools.OpenAPISpec())

	// Silence the browser's automatic favicon request.
	r.Get("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

// devLinks builds the links shown on the /dev launcher page.
func devLinks(cfg *config.Config) []devtools.Link {
	return []devtools.Link{
		{Title: "GraphQL Playground", Desc: "Explore & run GraphQL queries/subscriptions", URL: "/playground", Icon: "◈"},
		{Title: "Swagger / OpenAPI", Desc: "REST API reference", URL: "/swagger", Icon: "❡"},
		{Title: "Health", Desc: "Liveness of DB, Redis & memory", URL: "/health", Icon: "♥"},
		{Title: "pgweb (DB browser)", Desc: "Browse Postgres tables — Prisma Studio analog", URL: localhostURL(cfg.DBStudioPort), Icon: "⛁"},
		{Title: "RedisInsight", Desc: "Inspect Redis keys & streams", URL: localhostURL(cfg.RedisStudioPort), Icon: "⚡"},
		{Title: "Asynqmon (queue dashboard)", Desc: "Background jobs — Bull Board analog", URL: localhostURL(cfg.AsynqmonPort), Icon: "⚙"},
	}
}

// localhostURL builds a browser-reachable URL for a companion service that is
// published on the host loopback at the given port.
func localhostURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}
