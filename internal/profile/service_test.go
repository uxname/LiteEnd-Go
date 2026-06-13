package profile

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/db/sqlc"
)

// fakeQuerier is an in-memory Querier.
type fakeQuerier struct {
	mu      sync.Mutex
	bySub   map[string]sqlc.Profile
	nextID  int32
	creates int
	updates int
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{bySub: map[string]sqlc.Profile{}, nextID: 1}
}

func (f *fakeQuerier) GetProfileByOIDCSub(_ context.Context, sub string) (sqlc.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.bySub[sub]
	if !ok {
		return sqlc.Profile{}, pgx.ErrNoRows
	}
	return p, nil
}

func (f *fakeQuerier) CreateProfile(_ context.Context, sub string) (sqlc.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	p := sqlc.Profile{ID: f.nextID, OidcSub: sub, Roles: []sqlc.ProfileRole{sqlc.ProfileRoleUSER}}
	f.nextID++
	f.bySub[sub] = p
	return p, nil
}

func (f *fakeQuerier) UpdateProfile(_ context.Context, arg sqlc.UpdateProfileParams) (sqlc.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	for sub, p := range f.bySub {
		if p.ID == arg.ID {
			if arg.DisplayName != nil {
				p.DisplayName = arg.DisplayName
			}
			if arg.Bio != nil {
				p.Bio = arg.Bio
			}
			if arg.AvatarUrl != nil {
				p.AvatarUrl = arg.AvatarUrl
			}
			f.bySub[sub] = p
			return p, nil
		}
	}
	return sqlc.Profile{}, pgx.ErrNoRows
}

func (f *fakeQuerier) CountProfiles(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.bySub)), nil
}

// fakeCache is an in-memory Cache.
type fakeCache struct {
	mu   sync.Mutex
	data map[string]string
	hits int
}

func newFakeCache() *fakeCache { return &fakeCache{data: map[string]string{}} }

func (c *fakeCache) GetString(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.data[key]
	if !ok {
		return "", context.Canceled // any non-nil error = miss
	}
	c.hits++
	return v, nil
}

func (c *fakeCache) SetString(_ context.Context, key, value string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
	return nil
}

func (c *fakeCache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.data, k)
	}
	return nil
}

func newService() (*Service, *fakeQuerier, *fakeCache) {
	q := newFakeQuerier()
	c := newFakeCache()
	return New(q, c, slog.New(slog.DiscardHandler)), q, c
}

func TestFindOrCreateBySub_CreatesThenCaches(t *testing.T) {
	t.Parallel()
	svc, q, c := newService()
	ctx := context.Background()

	p, err := svc.FindOrCreateBySub(ctx, "sub-1")
	require.NoError(t, err)
	require.Equal(t, int32(1), p.ID)
	require.Equal(t, 1, q.creates)
	require.Equal(t, []sqlc.ProfileRole{sqlc.ProfileRoleUSER}, p.Roles)

	// Second call must hit the cache (no new create, no DB read).
	_, err = svc.FindOrCreateBySub(ctx, "sub-1")
	require.NoError(t, err)
	require.Equal(t, 1, q.creates, "should not create again")
	require.Positive(t, c.hits, "second call should hit cache")
}

func TestFindOrCreateBySub_ExistingNoCreate(t *testing.T) {
	t.Parallel()
	svc, q, _ := newService()
	ctx := context.Background()
	_, _ = q.CreateProfile(ctx, "sub-2")

	p, err := svc.FindOrCreateBySub(ctx, "sub-2")
	require.NoError(t, err)
	require.Equal(t, "sub-2", p.OidcSub)
	require.Equal(t, 1, q.creates)
}

func TestUpdate_InvalidatesCache(t *testing.T) {
	t.Parallel()
	svc, _, c := newService()
	ctx := context.Background()
	created, err := svc.FindOrCreateBySub(ctx, "sub-3")
	require.NoError(t, err)

	name := "Bob"
	updated, err := svc.Update(ctx, created.ID, "sub-3", UpdateParams{DisplayName: &name})
	require.NoError(t, err)
	require.NotNil(t, updated.DisplayName)
	require.Equal(t, "Bob", *updated.DisplayName)

	// Cache must reflect the new value (refreshed, not stale).
	got, err := svc.FindBySub(ctx, "sub-3")
	require.NoError(t, err)
	require.NotNil(t, got.DisplayName)
	require.Equal(t, "Bob", *got.DisplayName)
	require.Contains(t, c.data, config.ProfileCacheKeyPrefix+"sub-3")
}

func TestFindOrCreateMockUser_HasAdminRole(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService()
	p, err := svc.FindOrCreateMockUser(context.Background())
	require.NoError(t, err)
	require.Equal(t, MockSub, p.OidcSub)
	require.Contains(t, p.Roles, sqlc.ProfileRoleADMIN)
	require.Contains(t, p.Roles, sqlc.ProfileRoleUSER)
	require.NotNil(t, p.AvatarUrl)
}

func TestCount(t *testing.T) {
	t.Parallel()
	svc, _, _ := newService()
	ctx := context.Background()
	_, _ = svc.FindOrCreateBySub(ctx, "a")
	_, _ = svc.FindOrCreateBySub(ctx, "b")
	n, err := svc.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
}
