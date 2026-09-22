package resolver_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/auth"
	"github.com/uxname/liteend-go/internal/config"
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

// fakeLinks stands in for *upload.Service: our own links live under
// storagePrefix, anything else belongs to somebody else.
type fakeLinks struct {
	signErr error
	// owned is the set of keys the caller uploaded; anything else is somebody
	// else's file.
	owned    map[string]int32
	ownerErr error
}

const storagePrefix = "https://cdn.example.test/uploads"

func (f fakeLinks) LinkFor(_ context.Context, key string) (string, error) {
	if f.signErr != nil {
		return "", f.signErr
	}
	return storagePrefix + "/" + key + "?X-Amz-Signature=fake", nil
}

func (f fakeLinks) KeyFromLink(link string) (string, bool) {
	key, found := strings.CutPrefix(link, storagePrefix+"/")
	if !found || key == "" {
		return "", false
	}
	key, _, _ = strings.Cut(key, "?")
	return key, true
}

func (f fakeLinks) PermanentLink(key string) string { return storagePrefix + "/" + key }

func (f fakeLinks) OwnedBy(_ context.Context, key string, profileID int32) (bool, error) {
	if f.ownerErr != nil {
		return false, f.ownerErr
	}
	owner, ok := f.owned[key]
	return ok && owner == profileID, nil
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

// testUserID is the profile userCtx authenticates as.
const testUserID int32 = 2

func userCtx() context.Context {
	return auth.WithUser(context.Background(), &sqlc.Profile{
		ID:      testUserID,
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

// --- UpdateProfile input validation ---
//
// The columns are unbounded TEXT and avatarUrl is a bare String scalar, so these
// limits are the only thing standing between user input and the database.

// rejectingProfiles fails the test if the resolver reaches the service layer —
// validation must short-circuit before any write.
func rejectingProfiles(t *testing.T) fakeProfiles {
	t.Helper()
	return fakeProfiles{
		updateFn: func(context.Context, int32, string, profile.UpdateParams) (sqlc.Profile, error) {
			t.Fatal("service must not be called when input validation fails")
			return sqlc.Profile{}, nil
		},
	}
}

func requireBadUserInput(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	require.Equal(t, "BAD_USER_INPUT", gqlErr.Extensions["code"])
	require.Equal(t, 400, gqlErr.Extensions["statusCode"])
}

func TestUpdateProfile_RejectsOverlongDisplayName(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", config.ProfileDisplayNameMaxLen+1)
	r := &resolver.Resolver{Profiles: rejectingProfiles(t), PubSub: &fakePubSub{}, Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{DisplayName: &long})
	requireBadUserInput(t, err)
}

func TestUpdateProfile_CountsRunesNotBytesInDisplayName(t *testing.T) {
	t.Parallel()
	// 100 multi-byte runes are 300 bytes but exactly at the limit — a byte-based
	// check would wrongly reject this.
	atLimit := strings.Repeat("я", config.ProfileDisplayNameMaxLen)
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error) {
			return sqlc.Profile{ID: id, OidcSub: sub, DisplayName: in.DisplayName}, nil
		},
	}
	r := &resolver.Resolver{Profiles: profiles, PubSub: &fakePubSub{}, Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{DisplayName: &atLimit})
	require.NoError(t, err)
}

func TestUpdateProfile_RejectsOverlongBio(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("b", config.ProfileBioMaxLen+1)
	r := &resolver.Resolver{Profiles: rejectingProfiles(t), PubSub: &fakePubSub{}, Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{Bio: &long})
	requireBadUserInput(t, err)
}

func TestUpdateProfile_RejectsOverlongAvatarURL(t *testing.T) {
	t.Parallel()
	long := "https://example.com/" + strings.Repeat("c", config.ProfileAvatarURLMaxLen)
	r := &resolver.Resolver{Profiles: rejectingProfiles(t), PubSub: &fakePubSub{}, Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &long})
	requireBadUserInput(t, err)
}

func TestUpdateProfile_RejectsNonHTTPAvatarURL(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		"/relative/avatar.png",       // not absolute
		"ftp://example.com/a.png",    // wrong scheme
		"javascript:alert(1)",        // opaque, no host
		"data:image/png;base64,AAAA", // data URI
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			url := bad
			r := &resolver.Resolver{Profiles: rejectingProfiles(t), PubSub: &fakePubSub{}, Log: discardLog()}
			_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &url})
			requireBadUserInput(t, err)
		})
	}
}

func TestUpdateProfile_AcceptsEmptyAvatarURLAsClearing(t *testing.T) {
	t.Parallel()
	empty := ""
	called := false
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, _ profile.UpdateParams) (sqlc.Profile, error) {
			called = true
			return sqlc.Profile{ID: id, OidcSub: sub}, nil
		},
	}
	r := &resolver.Resolver{Profiles: profiles, PubSub: &fakePubSub{}, Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &empty})
	require.NoError(t, err)
	require.True(t, called, "an empty avatarUrl clears the avatar and must reach the service")
}

func TestUpdateProfile_AcceptsValidHTTPSAvatarURL(t *testing.T) {
	t.Parallel()
	good := "https://cdn.example.com/avatars/alice.png"
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error) {
			require.NotNil(t, in.AvatarURL)
			require.Equal(t, good, *in.AvatarURL)
			return sqlc.Profile{ID: id, OidcSub: sub}, nil
		},
	}
	r := &resolver.Resolver{Profiles: profiles, PubSub: &fakePubSub{}, Log: discardLog()}
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &good})
	require.NoError(t, err)
}

// --- avatars: stored permanently, served signed ---

// The link a client hands back carries a signature that expires, so what gets
// stored is the permanent form — otherwise the avatar dies with the signature.
func TestUpdateProfile_StoresThePermanentAvatarLink(t *testing.T) {
	t.Parallel()
	var stored *string
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error) {
			stored = in.AvatarURL
			return sqlc.Profile{ID: id, OidcSub: sub, AvatarUrl: in.AvatarURL}, nil
		},
	}
	links := fakeLinks{owned: map[string]int32{"2026/01/02/03-04/pic.png": testUserID}}
	r := &resolver.Resolver{Profiles: profiles, PubSub: &fakePubSub{}, Files: links, Log: discardLog()}

	signed := storagePrefix + "/2026/01/02/03-04/pic.png?X-Amz-Signature=abc&X-Amz-Expires=900"
	out, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &signed})
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, storagePrefix+"/2026/01/02/03-04/pic.png", *stored,
		"the signature is stripped before the value is stored")
	require.NotNil(t, out.AvatarURL)
	require.Contains(t, *out.AvatarURL, "X-Amz-Signature=", "what comes back out is signed again")
}

func TestUpdateProfile_ForeignAvatarURLIsUntouched(t *testing.T) {
	t.Parallel()
	var stored *string
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error) {
			stored = in.AvatarURL
			return sqlc.Profile{ID: id, OidcSub: sub, AvatarUrl: in.AvatarURL}, nil
		},
	}
	r := &resolver.Resolver{Profiles: profiles, PubSub: &fakePubSub{}, Files: fakeLinks{}, Log: discardLog()}

	external := "https://i.pravatar.cc/300"
	out, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &external})
	require.NoError(t, err)
	require.Equal(t, &external, stored, "a URL that is not ours is stored as it is")
	require.Equal(t, &external, out.AvatarURL, "…and served as it is")
}

// A storage that cannot sign must not take the whole profile read down with it.
func TestUpdateProfile_UnsignableAvatarIsOmitted(t *testing.T) {
	t.Parallel()
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error) {
			return sqlc.Profile{ID: id, OidcSub: sub, AvatarUrl: in.AvatarURL}, nil
		},
	}
	r := &resolver.Resolver{
		Profiles: profiles,
		PubSub:   &fakePubSub{},
		Files: fakeLinks{
			signErr: errors.New("no credentials"),
			owned:   map[string]int32{"2026/01/02/03-04/pic.png": testUserID},
		},
		Log: discardLog(),
	}

	stored := storagePrefix + "/2026/01/02/03-04/pic.png"
	out, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &stored})
	require.NoError(t, err)
	require.Nil(t, out.AvatarURL, "an unsignable avatar is left out, the profile still reads")
}

// A key is not a secret — it rides in every link we hand out — so naming
// somebody else's file as your avatar must not get it signed for you.
func TestUpdateProfile_RefusesAFileTheCallerDoesNotOwn(t *testing.T) {
	t.Parallel()
	called := false
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, _ profile.UpdateParams) (sqlc.Profile, error) {
			called = true
			return sqlc.Profile{ID: id, OidcSub: sub}, nil
		},
	}
	r := &resolver.Resolver{
		Profiles: profiles,
		PubSub:   &fakePubSub{},
		// The key exists, and belongs to someone else.
		Files: fakeLinks{owned: map[string]int32{"2026/01/02/03-04/pic.png": testUserID + 1}},
		Log:   discardLog(),
	}

	theirs := storagePrefix + "/2026/01/02/03-04/pic.png?X-Amz-Signature=abc"
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &theirs})
	require.Error(t, err)
	require.Contains(t, err.Error(), "file you uploaded")
	require.False(t, called, "nothing may be written when the file is not the caller's")

	// Same for a key no upload ever recorded.
	r.Files = fakeLinks{owned: map[string]int32{}}
	_, err = r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &theirs})
	require.Error(t, err)
}

// A database that cannot answer the ownership question must fail the mutation,
// not quietly store the avatar.
func TestUpdateProfile_OwnershipLookupFailureIsFatal(t *testing.T) {
	t.Parallel()
	profiles := fakeProfiles{
		updateFn: func(_ context.Context, id int32, sub string, _ profile.UpdateParams) (sqlc.Profile, error) {
			return sqlc.Profile{ID: id, OidcSub: sub}, nil
		},
	}
	r := &resolver.Resolver{
		Profiles: profiles,
		PubSub:   &fakePubSub{},
		Files:    fakeLinks{ownerErr: errors.New("db down")},
		Log:      discardLog(),
	}

	link := storagePrefix + "/2026/01/02/03-04/pic.png"
	_, err := r.Mutation().UpdateProfile(userCtx(), model.ProfileUpdateInput{AvatarURL: &link})
	require.Error(t, err)
}

// A connection may hold only a few live subscriptions: each one pins a
// goroutine and a listener for as long as the socket lives.
func TestProfileUpdated_RespectsTheConnectionBudget(t *testing.T) {
	t.Parallel()
	r := &resolver.Resolver{PubSub: &fakePubSub{ch: make(chan sqlc.Profile)}, Log: slog.New(slog.DiscardHandler)}
	conn := resolver.WithSubscriptionBudget(userCtx(), 1)
	first, cancelFirst := context.WithCancel(conn)

	_, err := r.Subscription().ProfileUpdated(first)
	require.NoError(t, err)
	_, err = r.Subscription().ProfileUpdated(conn)
	require.ErrorContains(t, err, "too many active subscriptions")

	cancelFirst() // the first subscription ends and frees its slot
	require.Eventually(t, func() bool {
		sub, cancel := context.WithCancel(conn)
		defer cancel()
		_, err := r.Subscription().ProfileUpdated(sub)
		return err == nil
	}, time.Second, 5*time.Millisecond)
}
