package resolver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/uxname/liteend-go/internal/config"
	"github.com/uxname/liteend-go/internal/graph/model"
)

// badInput builds a client-facing validation error. The BAD_USER_INPUT code
// keeps the error out of the production error-masking path (it is safe to show),
// while the error presenter still attaches a requestId.
func badInput(msg string) *gqlerror.Error {
	return clientError(msg, "BAD_USER_INPUT", http.StatusBadRequest)
}

// blankChars is what a profile field may hold and still count as blank, stored as
// NULL: the same set as the pattern in UpdateProfile (db/queries/profile.sql).
const blankChars = " \t\n\v\f\r"

// validateProfileUpdate enforces length and format limits on profile fields
// before they are persisted (the columns are unbounded TEXT, and URL is a bare
// String scalar, so the database does not constrain them).
func validateProfileUpdate(in model.ProfileUpdateInput) error {
	// docc:begin profile#^5mqw@bnce ~258y
	if in.DisplayName != nil && utf8.RuneCountInString(*in.DisplayName) > config.ProfileDisplayNameMaxLen {
		return badInput(fmt.Sprintf("displayName must be at most %d characters", config.ProfileDisplayNameMaxLen))
	}
	if in.Bio != nil && utf8.RuneCountInString(*in.Bio) > config.ProfileBioMaxLen {
		return badInput(fmt.Sprintf("bio must be at most %d characters", config.ProfileBioMaxLen))
	}
	// docc:end
	if in.AvatarURL != nil {
		if utf8.RuneCountInString(*in.AvatarURL) > config.ProfileAvatarURLMaxLen {
			return badInput(fmt.Sprintf("avatarUrl must be at most %d characters", config.ProfileAvatarURLMaxLen))
		}
		// A blank avatar is no URL to check: the store clears it to "not set".
		if strings.Trim(*in.AvatarURL, blankChars) != "" {
			u, err := url.Parse(*in.AvatarURL)
			if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
				return badInput("avatarUrl must be a valid absolute http(s) URL")
			}
		}
	}
	return nil
}
