// Package graph wires the gqlgen server: transports, WebSocket auth, error
// presenter, logging, and the GraphQL playground.
package graph

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

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
	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/graph/generated"
	"github.com/uxname/liteend-go/internal/graph/resolver"
	"github.com/uxname/liteend-go/internal/logger"
)

// wsCloseUnauthorized closes a WebSocket whose connection_init carried no
// valid credentials (4403 mirrors HTTP 403, as graphql-ws clients expect).
const wsCloseUnauthorized = 4403

var errUnauthorizedSocket = errors.New("unauthorized")

// credsAuthenticator is the slice of auth.Middleware the WebSocket init needs.
type credsAuthenticator interface {
	AuthenticateCreds(ctx context.Context, bearer, mockSub string) (*sqlc.Profile, time.Time)
}

// wsLimits bounds a WebSocket's lifetime. NewHandler uses the config values;
// tests shorten them.
type wsLimits struct {
	initTimeout time.Duration // connection_init must arrive within this
	pingPong    time.Duration // idle sockets that stop answering pings are closed
	tokenGrace  time.Duration // how long a socket may outlive its bearer token
}

type connCancelKey struct{}

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
	return newHandler(r, mw, isProd, allowedOrigins, wsLimits{
		initTimeout: config.WSInitTimeout,
		pingPong:    config.WSPingPongInterval,
		tokenGrace:  config.OIDCClockSkew,
	})
}

func newHandler(
	r *resolver.Resolver,
	mw credsAuthenticator,
	isProd bool,
	allowedOrigins []string,
	limits wsLimits,
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
	//
	// A socket is a long-lived resource the HTTP timeouts no longer cover once
	// the connection is hijacked, so it is bounded here instead: init must come
	// within initTimeout, an idle graphql-transport-ws socket that stops
	// answering pings is closed (KeepAlivePingInterval only serves the legacy
	// protocol), and every frame is capped.
	readLimit := int64(config.WSPayloadReadLimit)
	srv.AddTransport(&transport.Websocket{
		KeepAlivePingInterval: config.WSKeepAlivePingInterval,
		InitTimeout:           limits.initTimeout,
		PingPongInterval:      limits.pingPong,
		PayloadReadLimit:      &readLimit,
		Implementation:        transport.CoderWebsocketImplementation{AcceptOptions: accept},
		InitFunc: func(ctx context.Context, initPayload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
			bearer := auth.StripBearer(initPayload.Authorization())
			mockSub := initPayload.GetString("x-mock-sub")
			user, expiresAt := mw.AuthenticateCreds(ctx, bearer, mockSub)
			if user == nil {
				// Every operation over a socket needs a user (the one subscription
				// requires auth; anonymous queries have HTTP), so an anonymous
				// socket is only ever a held resource. Refuse it at init.
				ctx = transport.WithWebsocketCloseCode(ctx, wsCloseUnauthorized)
				return transport.AppendCloseReason(ctx, "unauthorized"), nil, errUnauthorizedSocket
			}
			ctx = auth.WithUser(ctx, user)
			if !expiresAt.IsZero() {
				// The socket must not outlive the token that opened it: gqlgen
				// closes the connection when this context ends.
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, expiresAt.Add(limits.tokenGrace))
				ctx = context.WithValue(ctx, connCancelKey{}, cancel)
				ctx = transport.AppendCloseReason(ctx, "token expired")
			}
			return ctx, &initPayload, nil
		},
		CloseFunc: func(ctx context.Context, _ int) {
			if cancel, ok := ctx.Value(connCancelKey{}).(context.CancelFunc); ok {
				cancel()
			}
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
