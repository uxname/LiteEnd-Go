-- +goose Up
-- +goose StatementBegin
CREATE TYPE profile_role AS ENUM ('ADMIN', 'USER');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE profiles (
    id           SERIAL PRIMARY KEY,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    oidc_sub     TEXT NOT NULL,
    roles        profile_role[] NOT NULL DEFAULT ARRAY['USER']::profile_role[],
    avatar_url   TEXT,
    display_name TEXT,
    bio          TEXT
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX profiles_oidc_sub_key ON profiles (oidc_sub);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE uploads (
    id                SERIAL PRIMARY KEY,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    filepath          TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    extension         TEXT NOT NULL,
    size              INTEGER NOT NULL,
    mimetype          TEXT NOT NULL,
    uploader_ip       TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX uploads_filepath_key ON uploads (filepath);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX uploads_uploader_ip_idx ON uploads (uploader_ip);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX uploads_created_at_idx ON uploads (created_at);
-- +goose StatementEnd

-- Auto-maintain updated_at (replaces Prisma @updatedAt).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER profiles_set_updated_at
    BEFORE UPDATE ON profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER uploads_set_updated_at
    BEFORE UPDATE ON uploads
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS uploads;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS profiles;
-- +goose StatementEnd
-- +goose StatementBegin
DROP FUNCTION IF EXISTS set_updated_at();
-- +goose StatementEnd
-- +goose StatementBegin
DROP TYPE IF EXISTS profile_role;
-- +goose StatementEnd
