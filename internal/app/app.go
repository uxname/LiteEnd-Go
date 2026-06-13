// Package app wires all components into a runnable application. It is used by
// the server entrypoint and by integration tests, so both exercise identical
// wiring.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

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
	r := srv.Router()

	// Health.
	r.Get("/health", health.New(database, rdb).Handler())

	// Domain services.
	profiles := profile.New(database.Queries, rdb, log)
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
	gqlHandler := graph.NewHandler(res, authMW, log)
	r.With(translator.Middleware, authMW.Optional).Handle("/graphql", gqlHandler)

	// File upload (REST).
	uploadH := upload.NewHandler(upload.New(database.Queries, log))
	uploadH.Register(r, authMW.RequireAuth)

	// Dev tools & API docs. These pages load CDN assets + inline scripts, so they
	// run under a relaxed CSP (the strict policy still applies to the API).
	devLinks := []devtools.Link{
		{Title: "GraphQL Playground", Desc: "Explore & run GraphQL queries/subscriptions", URL: "/playground"},
		{Title: "Swagger / OpenAPI", Desc: "REST API reference", URL: "/swagger"},
		{Title: "Health", Desc: "Liveness of DB, Redis & memory", URL: "/health"},
		{Title: "pgweb (DB browser)", Desc: "Browse Postgres tables — Prisma Studio analog", URL: localhostURL(cfg.DBStudioPort)},
		{Title: "RedisInsight", Desc: "Inspect Redis keys & streams", URL: localhostURL(cfg.RedisStudioPort)},
		{Title: "Asynqmon (queue dashboard)", Desc: "Background jobs — Bull Board analog", URL: localhostURL(cfg.AsynqmonPort)},
	}
	// Dev pages require Basic Auth — no anonymous access (same creds as the
	// external dashboards behind the auth proxy).
	devAuth := appmw.BasicAuth("liteend dev tools", cfg.AdminUser, cfg.AdminPassword)
	r.With(devAuth, devtools.RelaxCSP).Get("/playground", graph.Playground("/graphql"))
	r.With(devAuth, devtools.RelaxCSP).Get("/dev", devtools.DevLauncher(devLinks))
	r.With(devAuth, devtools.RelaxCSP).Get("/swagger", devtools.SwaggerUI("/openapi.json"))
	r.With(devAuth).Get("/openapi.json", devtools.OpenAPISpec())

	// Silence the browser's automatic favicon request.
	r.Get("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	app.Server = srv
	return app, nil
}

// localhostURL builds a browser-reachable URL for a companion service that is
// published on the host loopback at the given port.
func localhostURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}
