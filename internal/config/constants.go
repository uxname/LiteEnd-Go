package config

import "time"

// Ported 1:1 from the TypeScript src/common/constants.ts to preserve behaviour.
const (
	// RateLimitMax is the maximum number of requests allowed per RateLimitWindow.
	RateLimitMax = 100
	// RateLimitWindow is the sliding window for rate limiting.
	RateLimitWindow = time.Minute

	// CompressionThreshold is the minimum response size (bytes) before gzip kicks in.
	CompressionThreshold = 1024
	// BodyLimit is the maximum accepted request body size (10 MiB).
	BodyLimit = 10 * 1024 * 1024

	// HeapThresholdMB is the heap usage health threshold in megabytes.
	HeapThresholdMB = 150
	// DiskThreshold is the disk usage health threshold (fraction).
	DiskThreshold = 0.9

	// FallbackRequestID is used when no request id is present.
	FallbackRequestID = "unknown"

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
