-- +goose Up
-- +goose StatementBegin
-- Who uploaded the file. Until now an upload recorded only the address it came
-- from, so nothing could answer "is this object this user's?" — and the API
-- signs a download link for whatever object key a signed-in caller names.
--
-- Nullable on purpose: rows written before this column existed have no owner to
-- backfill, and an upload whose owner profile is later deleted keeps its row.
-- Both cases read as "owned by nobody", which the ownership check refuses.
ALTER TABLE uploads
    ADD COLUMN uploader_profile_id INTEGER REFERENCES profiles (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX uploads_uploader_profile_id_idx ON uploads (uploader_profile_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS uploads_uploader_profile_id_idx;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE uploads DROP COLUMN IF EXISTS uploader_profile_id;
-- +goose StatementEnd
