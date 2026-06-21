package resolver

import (
	"fmt"
	"net/url"
	"unicode/utf8"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/graph/model"
)

// badInput builds a client-facing validation error. The BAD_USER_INPUT code
// keeps the error out of the production error-masking path (it is safe to show),
// while the error presenter still attaches a requestId.
func badInput(msg string) *gqlerror.Error {
	return &gqlerror.Error{
		Message:    msg,
		Extensions: map[string]any{"code": "BAD_USER_INPUT", "statusCode": 400},
	}
}

// validateProfileUpdate enforces length and format limits on profile fields
// before they are persisted (the columns are unbounded TEXT, and URL is a bare
// String scalar, so the database does not constrain them).
func validateProfileUpdate(in model.ProfileUpdateInput) error {
	if in.DisplayName != nil && utf8.RuneCountInString(*in.DisplayName) > config.ProfileDisplayNameMaxLen {
		return badInput(fmt.Sprintf("displayName must be at most %d characters", config.ProfileDisplayNameMaxLen))
	}
	if in.Bio != nil && utf8.RuneCountInString(*in.Bio) > config.ProfileBioMaxLen {
		return badInput(fmt.Sprintf("bio must be at most %d characters", config.ProfileBioMaxLen))
	}
	if in.AvatarURL != nil {
		if utf8.RuneCountInString(*in.AvatarURL) > config.ProfileAvatarURLMaxLen {
			return badInput(fmt.Sprintf("avatarUrl must be at most %d characters", config.ProfileAvatarURLMaxLen))
		}
		if *in.AvatarURL != "" {
			u, err := url.Parse(*in.AvatarURL)
			if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
				return badInput("avatarUrl must be a valid absolute http(s) URL")
			}
		}
	}
	return nil
}
