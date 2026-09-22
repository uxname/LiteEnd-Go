// Package server wires the chi router, middleware stack, and HTTP lifecycle.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/httperr"
	appmw "github.com/uxname/liteend-go/internal/middleware"
)

// Server holds the router and configuration for the HTTP application.
type Server struct {
	cfg    *config.Config
	log    *slog.Logger
	router *chi.Mux
}

// New builds a Server with the base middleware stack applied.
func New(cfg *config.Config, log *slog.Logger, rdb *redis.Client) *Server {
	r := chi.NewRouter()

	// Order: request-id → context-logger → real-ip → logging → recovery →
	// secure-headers → compression → rate-limit → CORS → body-limit.
	//
	// RequestLogger must wrap Recoverer, not the other way round: a panic
	// unwinds past every logging call above it, so with the recoverer on the
	// outside a panicking request produced `panic_recovered` and NO
	// `http_request` line at all. Inside, the panic is turned into a 500 before
	// the access log runs, so the worst failures are the ones you can still find
	// by method/path/status.
	r.Use(requestID)
	r.Use(appmw.ContextLogger(log))           // request-scoped logger (request_id) for logger.From(ctx)
	r.Use(appmw.RealIP(cfg.TrustedProxyHops)) // client address from X-Forwarded-For
	r.Use(appmw.RequestLogger(log))
	r.Use(appmw.Recoverer(log))
	r.Use(appmw.SecureHeaders(cfg.IsProduction()))
	r.Use(chimw.Compress(5))
	if rdb != nil {
		r.Use(appmw.RateLimit(rdb))
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigin,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Accept-Language", "x-mock-sub"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(chimw.RequestSize(config.BodyLimit))

	// Catch-all 404 (mirrors AppController).
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httperr.Write(w, http.StatusNotFound, "Not Found")
	})

	return &Server{cfg: cfg, log: log, router: r}
}

// clientRequestID is the shape of a request id a client may choose.
var clientRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// requestID assigns the request its correlation id and echoes it back. A
// client-supplied X-Request-Id is kept only when short and plain: the id rides
// on every log line, GraphQL error and job payload of the request, so anything
// else is replaced (a 200 KB header used to reach the log in full). Stored
// under chi's key, so every middleware.GetReqID reader keeps working.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(chimw.RequestIDHeader)
		if !clientRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(chimw.RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chimw.RequestIDKey, id)))
	})
}

// Router exposes the underlying chi router so feature packages can mount routes.
func (s *Server) Router() *chi.Mux { return s.router }

// Run starts the HTTP server and blocks until ctx is cancelled, then performs
// a graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", s.cfg.Port),
		Handler:           s.router,
		ReadHeaderTimeout: config.ServerReadHeaderTimeout,
		ReadTimeout:       config.ServerReadTimeout,
		WriteTimeout:      config.ServerWriteTimeout,
		IdleTimeout:       config.ServerIdleTimeout,
		MaxHeaderBytes:    config.ServerMaxHeaderBytes,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutting down server")
		// ctx is already cancelled (that's why we're here); detach its
		// cancellation but keep its values, then give shutdown a fresh deadline.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.ServerShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown: %w", err)
		}
		return nil
	}
}
