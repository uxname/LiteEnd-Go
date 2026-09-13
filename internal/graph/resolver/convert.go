package resolver

import (
	"context"
	"fmt"
	"strconv"

	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/graph/model"
)

// toModelProfile converts a persistence Profile into the GraphQL model. The
// avatar is the one field that is not stored the way it is served — see
// avatarLink.
func (r *Resolver) toModelProfile(ctx context.Context, p sqlc.Profile) *model.Profile {
	return &model.Profile{
		ID:          strconv.FormatInt(int64(p.ID), 10),
		CreatedAt:   p.CreatedAt.Time,
		UpdatedAt:   p.UpdatedAt.Time,
		OidcSub:     p.OidcSub,
		Roles:       p.Roles,
		AvatarURL:   r.avatarLink(ctx, p.AvatarUrl),
		DisplayName: p.DisplayName,
		Bio:         p.Bio,
	}
}

// avatarLink turns the stored avatar reference into a URL the browser can load.
//
// A reference to our own storage is re-signed on every read: in private file
// mode the stored form is a bare address the storage refuses, and the signature
// that makes it work expires (config.FILE_LINK_TTL_MINUTES). Anything else — an
// OIDC `picture`, the mock avatar — is somebody else's URL and is passed
// through untouched. A signing failure hides the avatar rather than failing the
// whole profile read, and says so in the log.
func (r *Resolver) avatarLink(ctx context.Context, stored *string) *string {
	if stored == nil || r.Files == nil {
		return stored
	}
	key, ours := r.Files.KeyFromLink(*stored)
	if !ours {
		return stored
	}
	link, err := r.Files.LinkFor(ctx, key)
	if err != nil {
		r.Log.Warn("avatar link could not be signed", "key", key, "error", err.Error())
		return nil
	}
	return &link
}

// storedAvatar is the inverse: the permanent form of whatever link the client
// sent back. A client hands back the link it just got from POST /upload —
// signature, expiry and all — and storing that would store a value that stops
// working. Links to other hosts are kept as they are.
//
// It is also where the file is checked to be the caller's. A key is not a
// secret: it rides in every link we hand out, so it can be read off a
// screenshot or an expired URL. Without this check, naming someone else's key
// as your avatar would have the API sign a fresh, working link for it — which
// would make "a leaked link stops working" true only for people without an
// account.
func (r *Resolver) storedAvatar(ctx context.Context, profileID int32, link *string) (*string, error) {
	if link == nil || r.Files == nil {
		return link, nil
	}
	key, ours := r.Files.KeyFromLink(*link)
	if !ours {
		return link, nil
	}
	owned, err := r.Files.OwnedBy(ctx, key, profileID)
	if err != nil {
		return nil, fmt.Errorf("check avatar ownership: %w", err)
	}
	if !owned {
		return nil, badInput("avatarUrl must be a file you uploaded")
	}
	permanent := r.Files.PermanentLink(key)
	return &permanent, nil
}
