-- name: GetProfileByOIDCSub :one
SELECT * FROM profiles WHERE oidc_sub = $1;

-- name: GetProfileByID :one
SELECT * FROM profiles WHERE id = $1;

-- name: CreateProfile :one
INSERT INTO profiles (oidc_sub)
VALUES ($1)
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
