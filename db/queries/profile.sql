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
UPDATE profiles
SET
    avatar_url   = COALESCE(sqlc.narg('avatar_url'), avatar_url),
    display_name = COALESCE(sqlc.narg('display_name'), display_name),
    bio          = COALESCE(sqlc.narg('bio'), bio)
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: CountProfiles :one
SELECT count(*) FROM profiles;
