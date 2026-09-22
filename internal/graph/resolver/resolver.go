// Package resolver implements the GraphQL resolvers.
package resolver

import (
	"context"
	"log/slog"
	"time"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/profile"
)

//go:generate go run github.com/99designs/gqlgen generate

// startTime marks process start for the debug resolver's uptime. It lives here
// (not in schema.resolvers.go) because gqlgen regeneration only preserves
// resolver method bodies, not arbitrary package-level declarations.
var startTime = time.Now() //nolint:gochecknoglobals // process start for uptime; must survive gqlgen regen

// ProfileService is the profile domain behaviour the resolvers depend on.
type ProfileService interface {
	Update(ctx context.Context, id int32, sub string, in profile.UpdateParams) (sqlc.Profile, error)
	Count(ctx context.Context) (int64, error)
}

type subscriptionBudgetKey struct{}

// WithSubscriptionBudget caps how many subscriptions opened with ctx may be
// live at once. The WebSocket transport sets it per connection: each live
// subscription pins goroutines and a listener for as long as the socket lives.
func WithSubscriptionBudget(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, subscriptionBudgetKey{}, make(chan struct{}, n))
}

// acquireSubscription takes a slot of ctx's subscription budget (always
// granted when ctx carries none). release gives the slot back.
func acquireSubscription(ctx context.Context) (release func(), ok bool) {
	slots, has := ctx.Value(subscriptionBudgetKey{}).(chan struct{})
	if !has {
		return func() {}, true
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

func tooManySubscriptions() *gqlerror.Error {
	return &gqlerror.Error{
		Message:    "too many active subscriptions on this connection",
		Extensions: map[string]any{"code": "TOO_MANY_SUBSCRIPTIONS", "statusCode": 429},
	}
}

// ProfilePubSub publishes/subscribes profile-updated events.
type ProfilePubSub interface {
	Publish(ctx context.Context, p sqlc.Profile) error
	SubscribeForUser(ctx context.Context, userID int32) <-chan sqlc.Profile
}

// FileLinks turns stored file references into links a browser can use, and back.
// Implemented by *upload.Service. It exists because a link is not a stored
// value: in private file mode every read issues a fresh, expiring one.
type FileLinks interface {
	// LinkFor is the download URL for an object key, signed when files are private.
	LinkFor(ctx context.Context, key string) (string, error)
	// KeyFromLink extracts the object key from a link of ours (signed or not),
	// reporting false for anything pointing elsewhere — an OIDC picture, say.
	KeyFromLink(link string) (string, bool)
	// PermanentLink is the non-expiring form of a key, the one kept in the database.
	PermanentLink(key string) string
	// OwnedBy reports whether the object under key was uploaded by this profile.
	OwnedBy(ctx context.Context, key string, profileID int32) (bool, error)
}

// Enqueuer adds jobs to the background queue (wired in the queue phase).
type Enqueuer interface {
	AddTestJob(ctx context.Context, message string) error
}

// Translator resolves i18n messages (wired in the i18n phase).
type Translator interface {
	Translate(ctx context.Context, key string, args map[string]string) string
}

// Resolver is the root resolver holding all dependencies.
type Resolver struct {
	Profiles ProfileService
	PubSub   ProfilePubSub
	Queue    Enqueuer
	I18n     Translator
	Files    FileLinks
	Log      *slog.Logger
}
