// Package graph wires the gqlgen server: transports, WebSocket auth, error
// presenter, logging, and the GraphQL playground.
package graph

import (
	"context"
	"net/http"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/graph/generated"
	"github.com/uxname/liteend-go/internal/graph/resolver"
)

// NewHandler builds the GraphQL HTTP handler (queries, mutations, subscriptions).
func NewHandler(r *resolver.Resolver, mw *auth.Middleware) http.Handler {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: r}))

	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.MultipartForm{})

	// WebSocket transport. gqlgen negotiates both the modern
	// "graphql-transport-ws" (graphql-ws lib) and legacy subprotocols, so the
	// SPA's graphql-ws client connects without changes.
	srv.AddTransport(&transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
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

	srv.SetQueryCache(lru.New[*ast.QueryDocument](1000))
	srv.Use(extension.Introspection{})
	srv.Use(extension.AutomaticPersistedQuery{Cache: lru.New[string](100)})
	srv.Use(&LoggingExtension{})

	srv.SetErrorPresenter(errorPresenter)

	return srv
}

// Playground returns the GraphQL IDE handler (replaces Altair).
func Playground(endpoint string) http.HandlerFunc {
	return playground.Handler("LiteEnd-Go GraphQL", endpoint)
}
