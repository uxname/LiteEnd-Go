-- name: GetProfileByOIDCSub :one
SELECT * FROM profiles WHERE oidc_sub = $1;

-- name: CreateProfile :one
-- An upsert, so that two replicas racing to create a first-time user both get
-- the row instead of one of them a unique violation. The DO UPDATE writes the
-- value that is already there: it exists only because DO NOTHING returns no row
-- on a conflict, and RETURNING has to yield one. It runs for the loser of the
-- race alone — which is also the only time it touches updated_at (the trigger).
INSERT INTO profiles (oidc_sub)
VALUES ($1)
ON CONFLICT (oidc_sub) DO UPDATE SET oidc_sub = EXCLUDED.oidc_sub
RETURNING *;

-- name: UpdateProfile :one
-- A field left out (NULL) keeps its value; a field sent empty or blank (only spaces,
-- tabs, line breaks) is cleared to NULL — "not set" — so every reader sees one value
-- for it, and the account page falls back to the name from the sign-in provider
-- instead of showing an empty one. Any other value is stored exactly as sent, not
-- trimmed. The blank set matches blankChars in internal/graph/resolver/validate.go.
-- A blank value stored before this rule stays until the person edits that field.
UPDATE profiles
SET
    avatar_url   = CASE WHEN sqlc.narg('avatar_url')::text IS NULL THEN avatar_url
                        WHEN sqlc.narg('avatar_url')::text ~ '^[ \t\n\v\f\r]*$' THEN NULL
                        ELSE sqlc.narg('avatar_url')::text END,
    display_name = CASE WHEN sqlc.narg('display_name')::text IS NULL THEN display_name
                        WHEN sqlc.narg('display_name')::text ~ '^[ \t\n\v\f\r]*$' THEN NULL
                        ELSE sqlc.narg('display_name')::text END,
    bio          = CASE WHEN sqlc.narg('bio')::text IS NULL THEN bio
                        WHEN sqlc.narg('bio')::text ~ '^[ \t\n\v\f\r]*$' THEN NULL
                        ELSE sqlc.narg('bio')::text END
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: CountProfiles :one
SELECT count(*) FROM profiles;
