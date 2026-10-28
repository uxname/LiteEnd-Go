-- +goose Up
-- +goose StatementBegin
-- An empty display name, bio or avatar means "not set", which is NULL: UpdateProfile
-- stores it so from now on. Rows written before keep an empty string; this makes
-- them read the same. The down step does nothing: an empty string and
-- NULL mean the same thing, so there is nothing to put back.
UPDATE profiles
SET
    avatar_url   = NULLIF(avatar_url, ''),
    display_name = NULLIF(display_name, ''),
    bio          = NULLIF(bio, '')
WHERE avatar_url = '' OR display_name = '' OR bio = '';
-- +goose StatementEnd

-- +goose Down
SELECT 1;
