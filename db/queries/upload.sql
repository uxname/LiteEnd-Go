-- name: CreateUpload :one
INSERT INTO uploads (
    filepath, original_filename, extension, size, mimetype, uploader_ip
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetUploadByFilepath :one
SELECT * FROM uploads WHERE filepath = $1;
