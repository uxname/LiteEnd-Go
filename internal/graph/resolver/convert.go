package resolver

import (
	"strconv"

	"github.com/uxname/liteend-go/internal/db/sqlc"
	"github.com/uxname/liteend-go/internal/graph/model"
)

// toModelProfile converts a persistence Profile into the GraphQL model.
func toModelProfile(p sqlc.Profile) *model.Profile {
	return &model.Profile{
		ID:          strconv.FormatInt(int64(p.ID), 10),
		CreatedAt:   p.CreatedAt.Time,
		UpdatedAt:   p.UpdatedAt.Time,
		OidcSub:     p.OidcSub,
		Roles:       p.Roles,
		AvatarURL:   p.AvatarUrl,
		DisplayName: p.DisplayName,
		Bio:         p.Bio,
	}
}
