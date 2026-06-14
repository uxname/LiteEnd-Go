// Package profile owns profile persistence: find-or-create by OIDC subject,
// Redis caching, mock users, and profile updates. It is shared by the auth
// middleware and the GraphQL resolvers.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// uniqueViolation is the PostgreSQL SQLSTATE for a unique-constraint violation.
const uniqueViolation = "23505"

// ErrProfileNotFound is returned by FindBySub when no profile exists for the
// given OIDC subject (distinct from a real lookup error).
var ErrProfileNotFound = errors.New("profile not found")

// MockSub is the OIDC subject of the default development mock user.
const MockSub = "mock-oidc-sub"

// MockAvatar matches the avatar used by the TypeScript mock user.
const MockAvatar = "https://i.pravatar.cc/300"

// Querier is the subset of sqlc methods this service needs (eases testing).
type Querier interface {
	GetProfileByOIDCSub(ctx context.Context, oidcSub string) (sqlc.Profile, error)
	CreateProfile(ctx context.Context, oidcSub string) (sqlc.Profile, error)
	UpdateProfile(ctx context.Context, arg sqlc.UpdateProfileParams) (sqlc.Profile, error)
	CountProfiles(ctx context.Context) (int64, error)
}

// Cache is the caching behaviour the service needs (satisfied by redis.Client).
type Cache interface {
	GetString(ctx context.Context, key string) (string, error)
	SetString(ctx context.Context, key, value string, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// Service provides profile operations with caching.
type Service struct {
	q     Querier
	cache Cache
	log   *slog.Logger
}

// New builds a profile Service.
func New(q Querier, cache Cache, log *slog.Logger) *Service {
	return &Service{q: q, cache: cache, log: log}
}

func cacheKey(sub string) string { return config.ProfileCacheKeyPrefix + sub }

// FindOrCreateBySub returns the profile for an OIDC subject, creating it if
// absent. Results are cached in Redis for ProfileCacheTTL.
func (s *Service) FindOrCreateBySub(ctx context.Context, sub string) (sqlc.Profile, error) {
	if p, ok := s.fromCache(ctx, sub); ok {
		return p, nil
	}

	p, err := s.q.GetProfileByOIDCSub(ctx, sub)
	if errors.Is(err, pgx.ErrNoRows) {
		p, err = s.createOrGet(ctx, sub)
		if err != nil {
			return sqlc.Profile{}, err
		}
	} else if err != nil {
		return sqlc.Profile{}, fmt.Errorf("get profile: %w", err)
	}

	s.toCache(ctx, p)
	return p, nil
}

// createOrGet creates a profile for sub, tolerating the find-or-create race: if
// a concurrent request inserts the same oidc_sub between our SELECT and INSERT,
// CreateProfile fails with a unique-constraint violation, so we re-read the row
// the winner created instead of surfacing a 500.
func (s *Service) createOrGet(ctx context.Context, sub string) (sqlc.Profile, error) {
	p, err := s.q.CreateProfile(ctx, sub)
	if err == nil {
		s.log.Info("profile created", "profileId", p.ID)
		return p, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		p, err = s.q.GetProfileByOIDCSub(ctx, sub)
		if err != nil {
			return sqlc.Profile{}, fmt.Errorf("get profile after create conflict: %w", err)
		}
		return p, nil
	}
	return sqlc.Profile{}, fmt.Errorf("create profile: %w", err)
}

// FindBySub returns a profile by subject, or ErrProfileNotFound if none exists.
// Note: this is a read-through cache — on a cache miss it populates the Redis
// cache as a side effect (an intentional caching optimisation, not a mutation
// of the profile itself).
func (s *Service) FindBySub(ctx context.Context, sub string) (*sqlc.Profile, error) {
	if p, ok := s.fromCache(ctx, sub); ok {
		return &p, nil
	}
	p, err := s.q.GetProfileByOIDCSub(ctx, sub)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProfileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	s.toCache(ctx, p)
	return &p, nil
}

// FindOrCreateMockUser returns the default mock admin user, creating it once.
// It always normalises roles to [USER, ADMIN] and the mock avatar.
func (s *Service) FindOrCreateMockUser(ctx context.Context) (sqlc.Profile, error) {
	p, err := s.q.GetProfileByOIDCSub(ctx, MockSub)
	if errors.Is(err, pgx.ErrNoRows) {
		p, err = s.createOrGet(ctx, MockSub)
		if err != nil {
			return sqlc.Profile{}, err
		}
	} else if err != nil {
		return sqlc.Profile{}, fmt.Errorf("get mock user: %w", err)
	}
	// Note: mock roles [USER, ADMIN] are applied at the auth layer (in-memory)
	// so we don't need a dedicated roles-update query; ensure avatar/roles here.
	p.Roles = []sqlc.ProfileRole{sqlc.ProfileRoleUSER, sqlc.ProfileRoleADMIN}
	avatar := MockAvatar
	p.AvatarUrl = &avatar
	return p, nil
}

// UpdateParams describes a profile update (nil fields are left unchanged).
type UpdateParams struct {
	AvatarURL   *string
	DisplayName *string
	Bio         *string
}

// Update applies a partial update by profile id, then invalidates the cache so
// subsequent reads reflect the change (prevents stale-cache after update).
func (s *Service) Update(ctx context.Context, id int32, sub string, in UpdateParams) (sqlc.Profile, error) {
	p, err := s.q.UpdateProfile(ctx, sqlc.UpdateProfileParams{
		ID:          id,
		AvatarUrl:   in.AvatarURL,
		DisplayName: in.DisplayName,
		Bio:         in.Bio,
	})
	if err != nil {
		return sqlc.Profile{}, fmt.Errorf("update profile: %w", err)
	}
	// Refresh cache with the new value.
	s.invalidate(ctx, sub)
	s.toCache(ctx, p)
	return p, nil
}

// Count returns the total number of profiles (used by the debug resolver).
func (s *Service) Count(ctx context.Context) (int64, error) {
	return s.q.CountProfiles(ctx)
}

func (s *Service) fromCache(ctx context.Context, sub string) (sqlc.Profile, bool) {
	raw, err := s.cache.GetString(ctx, cacheKey(sub))
	if err != nil {
		return sqlc.Profile{}, false
	}
	var p sqlc.Profile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		s.log.Warn("invalid JSON in profile cache, dropping")
		_ = s.cache.Delete(ctx, cacheKey(sub))
		return sqlc.Profile{}, false
	}
	return p, true
}

func (s *Service) toCache(ctx context.Context, p sqlc.Profile) {
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := s.cache.SetString(ctx, cacheKey(p.OidcSub), string(raw), config.ProfileCacheTTL); err != nil {
		s.log.Warn("profile cache set failed", "error", err)
	}
}

func (s *Service) invalidate(ctx context.Context, sub string) {
	_ = s.cache.Delete(ctx, cacheKey(sub))
}
