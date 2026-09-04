// Package graph wires the gqlgen server: transports, WebSocket auth, error
// presenter, logging, and the GraphQL playground.
package graph

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	coderws "github.com/coder/websocket"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/graph/generated"
	"github.com/uxname/liteend-go/internal/graph/resolver"
	"github.com/uxname/liteend-go/internal/logger"
)

// NewHandler builds the GraphQL HTTP handler (queries, mutations, subscriptions).
// isProd disables introspection and masks internal error messages in production.
// allowedOrigins is the HTTP CORS allowlist, reused to authorize cross-origin
// WebSocket handshakes; empty means no cross-origin handshake is allowed, in
// every environment.
func NewHandler(
	r *resolver.Resolver,
	mw *auth.Middleware,
	isProd bool,
	allowedOrigins []string,
) http.Handler {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: r}))

	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.MultipartForm{})

	// coder/websocket (gqlgen's default adapter since it dropped gorilla) only
	// authorizes same-origin handshakes, but the SPA lives on another origin — so
	// mirror the HTTP CORS allowlist. Patterns carrying a scheme are matched
	// against "scheme://host", which is exactly the CORS_ORIGIN format.
	// An empty list is never allow-all — the fail-fast on an empty CORS_ORIGIN
	// only fires for NODE_ENV=production, so a staging deployment used to accept
	// every origin. coder/websocket still lets same-origin and Origin-less
	// (non-browser) clients through, so only cross-origin browsers are refused.
	accept := coderws.AcceptOptions{OriginPatterns: allowedOrigins}

	// WebSocket transport. gqlgen negotiates both the modern
	// "graphql-transport-ws" (graphql-ws lib) and legacy subprotocols, so the
	// SPA's graphql-ws client connects without changes.
	srv.AddTransport(&transport.Websocket{
		KeepAlivePingInterval: config.WSKeepAlivePingInterval,
		Implementation:        transport.CoderWebsocketImplementation{AcceptOptions: accept},
		InitFunc: func(ctx context.Context, initPayload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
			bearer := auth.StripBearer(initPayload.Authorization())
			mockSub := initPayload.GetString("x-mock-sub")
			if user := mw.AuthenticateCreds(ctx, bearer, mockSub); user != nil {
				ctx = auth.WithUser(ctx, user)
			}
			return ctx, &initPayload, nil
		},
	})

	srv.SetQueryCache(lru.New[*ast.QueryDocument](config.GraphQLQueryCacheSize))
	// Introspection is a useful dev affordance but leaks the full schema; disable
	// it in production.
	if !isProd {
		srv.Use(extension.Introspection{})
	}
	srv.Use(extension.AutomaticPersistedQuery{Cache: lru.New[string](config.GraphQLAPQCacheSize)})
	// Bound the cost of a single operation so deeply nested/expensive queries
	// cannot exhaust server resources.
	srv.Use(extension.FixedComplexityLimit(config.GraphQLComplexityLimit))
	srv.Use(&LoggingExtension{})

	// A panic inside a resolver is recovered by gqlgen, never by
	// middleware.Recoverer — so `panic_recovered` does not fire for the single
	// most likely place to panic. gqlgen's default writes the value and the
	// stack to stderr with fmt.Fprintln + debug.PrintStack: raw text, no
	// request_id, and a shape a JSON log collector drops. Route it through the
	// request-scoped logger instead; the user-facing message is unchanged.
	srv.SetRecoverFunc(recoverPanic)

	srv.SetErrorPresenter(newErrorPresenter(isProd))

	return srv
}

// recoverPanic turns a recovered resolver panic into a structured, correlated
// log line and the same user-facing message gqlgen's default returns.
func recoverPanic(ctx context.Context, err any) error {
	logger.From(ctx).LogAttrs(ctx, slog.LevelError, "graphql_panic",
		slog.Any("panic", err),
		slog.String("stack", string(debug.Stack())))
	return gqlerror.Errorf("internal system error")
}

// Playground returns the GraphQL IDE handler (replaces Altair).
func Playground(endpoint string) http.HandlerFunc {
	return playground.Handler("LiteEnd-Go GraphQL", endpoint)
}
