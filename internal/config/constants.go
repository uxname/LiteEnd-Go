package config

import "time"

// Ported 1:1 from the TypeScript src/common/constants.ts to preserve behaviour.
const (
	// RateLimitMax is the maximum number of requests allowed per RateLimitWindow.
	RateLimitMax = 100
	// RateLimitWindow is the sliding window for rate limiting.
	RateLimitWindow = time.Minute

	// BodyLimit is the maximum accepted request body size (10 MiB).
	BodyLimit = 10 * 1024 * 1024

	// HeapThresholdMB is the heap usage health threshold in megabytes.
	HeapThresholdMB = 150
	// HealthCheckTimeout bounds the whole /health probe (all dependency pings).
	HealthCheckTimeout = 5 * time.Second

	// FallbackRequestID is used when no request id is present.
	FallbackRequestID = "unknown"

	// GraphQL handler tuning.
	// GraphQLComplexityLimit caps the cost of a single operation, bounding
	// resource use from deeply nested or expensive queries.
	GraphQLComplexityLimit = 200
	// GraphQLQueryCacheSize is the LRU size for parsed query documents.
	GraphQLQueryCacheSize = 1000
	// GraphQLAPQCacheSize is the LRU size for automatic persisted queries.
	GraphQLAPQCacheSize = 100
	// WSKeepAlivePingInterval is the WebSocket transport keep-alive ping interval.
	WSKeepAlivePingInterval = 10 * time.Second

	// Profile field limits (enforced before persistence).
	ProfileDisplayNameMaxLen = 100
	ProfileBioMaxLen         = 1000
	ProfileAvatarURLMaxLen   = 2048

	// HTTP server timeouts. ReadHeaderTimeout caps slow header sends; ReadTimeout
	// and WriteTimeout bound the full request/response (Slow Loris protection);
	// IdleTimeout reaps idle keep-alive connections. ShutdownTimeout bounds the
	// graceful-shutdown drain.
	ServerReadHeaderTimeout = 10 * time.Second
	ServerReadTimeout       = 10 * time.Second
	ServerWriteTimeout      = 30 * time.Second
	ServerIdleTimeout       = 60 * time.Second
	ServerShutdownTimeout   = 15 * time.Second

	// OIDCHTTPTimeout bounds JWKS/issuer fetches so a hung issuer cannot stall
	// every authenticated request indefinitely.
	OIDCHTTPTimeout = 5 * time.Second

	// Redis tuning.
	RedisConnectTimeout = 10 * time.Second
	RedisRetryMaxDelay  = 3 * time.Second
	RedisRetryBaseDelay = 200 * time.Millisecond

	// Database tuning.
	DBConnectTimeout = 10 * time.Second
	DBIdleTimeout    = 30 * time.Second
	DBPoolMax        = 10
	DBRetryMaxDelay  = 5 * time.Second
	DBRetryBaseDelay = 1 * time.Second
	DBMaxRetries     = 5
	// DBStatementTimeout caps any single SQL statement at the server, so a hung
	// or runaway query cannot hold a pooled connection indefinitely and exhaust
	// the pool (cascading failure protection).
	DBStatementTimeout = 30 * time.Second
	// DBMaxConnLifetime recycles connections periodically so the pool recovers
	// from stale server-side state and rebalances across replicas.
	DBMaxConnLifetime = time.Hour
	// DBHealthCheckPeriod is how often the pool probes idle connections.
	DBHealthCheckPeriod = time.Minute

	// FileUploadTimeout bounds a single upload request.
	FileUploadTimeout = 30 * time.Second

	// ProfileCacheTTL mirrors the 1h Redis cache for profiles.
	ProfileCacheTTL = time.Hour
	// ProfileCacheKeyPrefix is the Redis key prefix for cached profiles.
	ProfileCacheKeyPrefix = "profile:sub:"

	// Upload limits (per @fastify/multipart config).
	UploadMaxFileSize = 5 * 1024 * 1024
	UploadMaxFiles    = 10
)
