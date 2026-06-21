// Package graph wires the gqlgen server: transports, WebSocket auth, error
// presenter, logging, and the GraphQL playground.
package graph

import (
	"context"
	"net/http"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/graph/generated"
	"github.com/uxname/liteend-go/internal/graph/resolver"
)

// NewHandler builds the GraphQL HTTP handler (queries, mutations, subscriptions).
// isProd disables introspection and masks internal error messages in production.
func NewHandler(r *resolver.Resolver, mw *auth.Middleware, isProd bool) http.Handler {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: r}))

	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.MultipartForm{})

	// WebSocket transport. gqlgen negotiates both the modern
	// "graphql-transport-ws" (graphql-ws lib) and legacy subprotocols, so the
	// SPA's graphql-ws client connects without changes.
	srv.AddTransport(&transport.Websocket{
		KeepAlivePingInterval: config.WSKeepAlivePingInterval,
		Upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
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

	srv.SetErrorPresenter(newErrorPresenter(isProd))

	return srv
}

// Playground returns the GraphQL IDE handler (replaces Altair).
func Playground(endpoint string) http.HandlerFunc {
	return playground.Handler("LiteEnd-Go GraphQL", endpoint)
}
