// Package graph wires the gqlgen server: transports, WebSocket auth, error
// presenter, logging, and the GraphQL playground.
package graph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/99designs/gqlgen/graphql"
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
	"github.com/uxname/liteend-go/internal/middleware"
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

// opLimiter charges one event to a rate budget (middleware.Limiter).
type opLimiter interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration)
}

type (
	connCancelKey struct{}
	rateKeyKey    struct{}
	wsConnKey     struct{}
)

// NewHandler builds the GraphQL HTTP handler (queries, mutations, subscriptions).
// isProd disables introspection and masks internal error messages in production.
// allowedOrigins is the HTTP CORS allowlist, reused to authorize cross-origin
// WebSocket handshakes; empty means no cross-origin handshake is allowed, in
// every environment. limiter is the per-IP HTTP budget (nil without Redis):
// operations sent over a WebSocket are charged to it here, since the HTTP
// middleware only ever sees the upgrade request.
func NewHandler(
	r *resolver.Resolver,
	mw *auth.Middleware,
	limiter *middleware.Limiter,
	isProd bool,
	allowedOrigins []string,
) http.Handler {
	var lim opLimiter
	if limiter != nil {
		lim = limiter
	}
	return newHandler(r, mw, lim, isProd, allowedOrigins, wsLimits{
		initTimeout: config.WSInitTimeout,
		pingPong:    config.WSPingPongInterval,
		tokenGrace:  config.OIDCClockSkew,
	})
}

func newHandler(
	r *resolver.Resolver,
	mw credsAuthenticator,
	limiter opLimiter,
	isProd bool,
	allowedOrigins []string,
	limits wsLimits,
) http.Handler {
	srv := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: r}))

	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	// No MultipartForm transport: the schema has no Upload scalar (files go
	// through REST /upload), and multipart only offered a CORS-simple way to
	// send a mutation from another site.

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
			ctx = context.WithValue(auth.WithUser(ctx, user), wsConnKey{}, true)
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

	if limiter != nil {
		srv.AroundOperations(chargeWebsocketOperations(limiter))
	}

	// Bound the cost of a query before gqlgen spends anything on it: parsing and
	// validation run, and the validated document enters the query cache (keyed
	// by its full text), before FixedComplexityLimit ever sees the operation.
	// The byte cap also bounds what the caches can hold; the token cap bounds
	// the super-linear validation of small but dense queries.
	srv.Use(queryByteLimit(config.GraphQLMaxQueryBytes))
	srv.SetParserTokenLimit(config.GraphQLParserTokenLimit)
	// Suggestions ("Did you mean …") would enumerate the schema that disabled
	// introspection hides.
	srv.SetDisableSuggestion(isProd)
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

	// Remember the caller's rate key before a possible upgrade: RealIP has
	// already resolved the client address, and the socket's operations inherit
	// this request context.
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), rateKeyKey{}, middleware.RateKey(req))
		srv.ServeHTTP(w, req.WithContext(ctx))
	})
}

// queryByteLimit rejects a raw query longer than its value before it is parsed.
// Registered before the APQ extension, so an oversized query never enters the
// persisted-query cache either.
type queryByteLimit int

func (queryByteLimit) ExtensionName() string { return "QueryByteLimit" }

func (queryByteLimit) Validate(graphql.ExecutableSchema) error { return nil }

func (l queryByteLimit) MutateOperationParameters(_ context.Context, p *graphql.RawParams) *gqlerror.Error {
	if len(p.Query) <= int(l) {
		return nil
	}
	return &gqlerror.Error{
		Message:    fmt.Sprintf("query exceeds %d bytes", int(l)),
		Extensions: map[string]any{"code": "QUERY_TOO_LARGE", "statusCode": http.StatusRequestEntityTooLarge},
	}
}

// chargeWebsocketOperations spends one event of the upgrade request's rate
// budget per operation sent over a WebSocket. HTTP operations are skipped: the
// RateLimit middleware already charged their request.
func chargeWebsocketOperations(limiter opLimiter) graphql.OperationMiddleware {
	return func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		key, _ := ctx.Value(rateKeyKey{}).(string)
		if isWS, _ := ctx.Value(wsConnKey{}).(bool); !isWS || key == "" {
			return next(ctx)
		}
		if allowed, _ := limiter.Allow(ctx, key); allowed {
			return next(ctx)
		}
		// OneShot, not a bare response: a WebSocket keeps pulling the handler
		// until it returns nil, so anything else would loop.
		return graphql.OneShot(&graphql.Response{Errors: gqlerror.List{{
			Message:    "Too Many Requests",
			Extensions: map[string]any{"code": "TOO_MANY_REQUESTS", "statusCode": http.StatusTooManyRequests},
		}}})
	}
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
