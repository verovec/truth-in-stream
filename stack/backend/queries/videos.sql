-- name: CreateVideo :one
INSERT INTO videos (title, object_key, content_type, size_bytes, status, kind)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at;

-- name: GetVideo :one
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at
FROM videos
WHERE id = $1;

-- name: ListVideos :many
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at
FROM videos
ORDER BY created_at DESC, id;

-- name: SetVideoStatus :one
UPDATE videos
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at;

-- name: UpsertSampleVideo :one
INSERT INTO videos (title, object_key, content_type, size_bytes, status, kind)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (object_key) DO UPDATE
    SET title        = EXCLUDED.title,
        content_type = EXCLUDED.content_type,
        size_bytes   = EXCLUDED.size_bytes,
        status       = EXCLUDED.status,
        kind         = EXCLUDED.kind,
        updated_at   = now()
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at;
