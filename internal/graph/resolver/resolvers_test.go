package resolver_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/graph/model"
	"github.com/uxname/liteend-go/internal/graph/resolver"
	"github.com/uxname/liteend-go/internal/profile"
)

// --- fakes implementing the resolver's narrow dependency interfaces ---

type fakeProfiles struct {
	updateFn func(ctx context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error)
	countFn  func(ctx context.Context) (int64, error)
}

func (f fakeProfiles) Update(
	ctx context.Context, id int32, sub string, in profile.UpdateParams,
) (sqlc.Profile, error) {
	return f.updateFn(ctx, id, sub, in)
}

func (f fakeProfiles) Count(ctx context.Context) (int64, error) { return f.countFn(ctx) }

type fakePubSub struct {
	published  []sqlc.Profile
	publishErr error
	ch         chan sqlc.Profile
}

func (f *fakePubSub) Publish(_ context.Context, p sqlc.Profile) error {
	f.published = append(f.published, p)
	return f.publishErr
}

func (f *fakePubSub) SubscribeForUser(_ context.Context, _ int32) <-chan sqlc.Profile {
	return f.ch
}

type fakeEnqueuer struct {
	calls []string
	err   error
}

func (f *fakeEnqueuer) AddTestJob(_ context.Context, message string) error {
	f.calls = append(f.calls, message)
	return f.err
}

type fakeTranslator struct{ out string }

func (f fakeTranslator) Translate(_ context.Context, _ string, _ map[string]string) string {
	return f.out
}

// --- helpers ---

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

func adminCtx() context.Context {
	return auth.WithUser(context.Background(), &sqlc.Profile{
		ID:      1,
		OidcSub: "admin-sub",
		Roles:   []sqlc.ProfileRole{sqlc.ProfileRoleUSER, sqlc.ProfileRoleADMIN},
	})
}

func userCtx() context.Context {
	return auth.WithUser(context.Background(), &sqlc.Profile{
		ID:      2,
		OidcSub: "user-sub",
		Roles:   []sqlc.ProfileRole{sqlc.ProfileRoleUSER},
	})
}

// --- UpdateProfile (mutation) ---

func TestUpdateProfile_Unauthenticated(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(context.Background(), model.ProfileUpdateInput{})
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestUpdateProfile_SuccessPublishesEvent(t *testing.T) {
	t.Parallel()
	name := "Alice"
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error) {
			require.Equal(t, int32(2), id)
			require.Equal(t, "user-sub", sub)
			require.NotNil(t, in.DisplayName)
			require.Equal(t, "Alice", *in.DisplayName)
			return sqlc.Profile{ID: id, OidcSub: sub, DisplayName: &name}, nil
		},
	}
	ps := &fakePubSub{}
	r := &resolver.Resolver{Profiles: profiles, PubSub: ps, Log: discardLog()}

	out, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{DisplayName: &name})
	require.NoError(t, err)
	require.NotNil(t, out.DisplayName)
	require.Equal(t, "Alice", *out.DisplayName)
	require.Len(t, ps.published, 1, "should publish a profileUpdated event")
}

func TestUpdateProfile_ServiceError(t *testing.T) {
	t.Parallel()
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, _ int32, _ string, _ profile.UpdateParams) (sqlc.Profile, error) {
			return sqlc.Profile{}, errors.New("db down")
		},
	}
	r := &resolver.Resolver{Profiles: profiles, PubSub: &fakePubSub{}, Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{})
	require.Error(t, err)
}

func TestUpdateProfile_PublishErrorStillSucceeds(t *testing.T) {
	t.Parallel()
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, _ profile.UpdateParams) (sqlc.Profile, error) {
			return sqlc.Profile{ID: id, OidcSub: sub}, nil
		},
	}
	ps := &fakePubSub{publishErr: errors.New("redis down")}
	r := &resolver.Resolver{Profiles: profiles, PubSub: ps, Log: discardLog()}
	out, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{})
	require.NoError(t, err, "a publish failure is logged, not fatal")
	require.Equal(t, "user-sub", out.OidcSub)
}

// --- AddTestJob (mutation) ---

func TestAddTestJob_Unauthenticated(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Queue: &fakeEnqueuer{}, Log: discardLog()}
	ok, err := r.Mutation().AddTestJob(context.Background(), "hi")
	require.Error(t, err)
	require.False(t, ok)
}

func TestAddTestJob_QueueUnavailable(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	ok, err := r.Mutation().AddTestJob(userCtx(), "hi")
	require.Error(t, err)
	require.False(t, ok)
}

func TestAddTestJob_Success(t *testing.T) {
	t.Parallel()
	q := &fakeEnqueuer{}
	r := &resolver.Resolver{Queue: q, Log: discardLog()}
	ok, err := r.Mutation().AddTestJob(userCtx(), "ping")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"ping"}, q.calls)
}

func TestAddTestJob_EnqueueError(t *testing.T) {
	t.Parallel()
	q := &fakeEnqueuer{err: errors.New("enqueue failed")}
	r := &resolver.Resolver{Queue: q, Log: discardLog()}
	ok, err := r.Mutation().AddTestJob(userCtx(), "ping")
	require.Error(t, err)
	require.False(t, ok)
}

// --- Echo (admin-only, present on both mutation and query) ---

func TestEchoMutation_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Mutation().Echo(userCtx(), "x")
	require.ErrorIs(t, err, auth.ErrForbidden)
}

func TestEchoMutation_AdminEchoesText(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	out, err := r.Mutation().Echo(adminCtx(), "hello")
	require.NoError(t, err)
	require.Equal(t, "hello", out)
}

func TestEchoQuery_AdminEchoesText(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	out, err := r.Query().Echo(adminCtx(), "ping")
	require.NoError(t, err)
	require.Equal(t, "ping", out)
}

// --- Me (query) ---

func TestMe_Unauthenticated(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Query().Me(context.Background())
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestMe_ReturnsAuthenticatedProfile(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	out, err := r.Query().Me(userCtx())
	require.NoError(t, err)
	require.Equal(t, "user-sub", out.OidcSub)
}

// --- TestTranslation (admin-only query) ---

func TestTestTranslation_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Query().TestTranslation(userCtx(), "bob")
	require.ErrorIs(t, err, auth.ErrForbidden)
}

func TestTestTranslation_UnavailableWhenNoTranslator(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Query().TestTranslation(adminCtx(), "bob")
	require.Error(t, err)
}

func TestTestTranslation_ReturnsTranslated(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{I18n: fakeTranslator{out: "Hello bob"}, Log: discardLog()}
	out, err := r.Query().TestTranslation(adminCtx(), "bob")
	require.NoError(t, err)
	require.Equal(t, "Hello bob", out)
}

// --- Debug (admin-only query) ---

func TestDebug_ForbiddenForNonAdmin(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Query().Debug(userCtx())
	require.ErrorIs(t, err, auth.ErrForbidden)
}

func TestDebug_ReturnsTotalUsers(t *testing.T) {
	t.Parallel()
	profiles := fakeProfiles{countFn: func(_ context.Context) (int64, error) { return 5, nil }}
	r := &resolver.Resolver{Profiles: profiles, Log: discardLog()}
	out, err := r.Query().Debug(adminCtx())
	require.NoError(t, err)
	require.Equal(t, int64(5), out["totalUsers"])
}

func TestDebug_CountError(t *testing.T) {
	t.Parallel()
	profiles := fakeProfiles{countFn: func(_ context.Context) (int64, error) { return 0, errors.New("count failed") }}
	r := &resolver.Resolver{Profiles: profiles, Log: discardLog()}
	_, err := r.Query().Debug(adminCtx())
	require.Error(t, err)
}

// --- ProfileUpdated (subscription) ---

func TestProfileUpdated_Unauthenticated(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Subscription().ProfileUpdated(context.Background())
	require.ErrorIs(t, err, auth.ErrUnauthenticated)
}

func TestProfileUpdated_UnavailableWhenNoPubSub(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{Log: discardLog()}
	_, err := r.Subscription().ProfileUpdated(userCtx())
	require.Error(t, err)
}

func TestProfileUpdated_BridgesEvents(t *testing.T) {
	t.Parallel()
	name := "Carol"
	ch := make(chan sqlc.Profile, 1)
	r := &resolver.Resolver{PubSub: &fakePubSub{ch: ch}, Log: discardLog()}

	out, err := r.Subscription().ProfileUpdated(userCtx())
	require.NoError(t, err)

	ch <- sqlc.Profile{ID: 2, OidcSub: "user-sub", DisplayName: &name}
	select {
	case got := <-out:
		require.NotNil(t, got)
		require.Equal(t, "user-sub", got.OidcSub)
		require.Equal(t, "Carol", *got.DisplayName)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the bridged profileUpdated event")
	}
	close(ch)
}
