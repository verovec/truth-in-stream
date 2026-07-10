-- name: CreateTVChannel :one
INSERT INTO tv_channels (slug, name, source_kind, source_ref, enabled, archive_enabled)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, slug, name, source_kind, source_ref, enabled, archive_enabled, created_at, updated_at;

-- name: GetTVChannel :one
SELECT id, slug, name, source_kind, source_ref, enabled, archive_enabled, created_at, updated_at
FROM tv_channels
WHERE id = $1;

-- name: ListTVChannels :many
SELECT id, slug, name, source_kind, source_ref, enabled, archive_enabled, created_at, updated_at
FROM tv_channels
ORDER BY name, id;

-- name: UpdateTVChannel :one
-- Write the mutable fields of an existing channel. slug is immutable (it keys
-- storage paths and seeds), so it is not updatable here.
UPDATE tv_channels
SET name = $2, source_kind = $3, source_ref = $4, enabled = $5, archive_enabled = $6, updated_at = now()
WHERE id = $1
RETURNING id, slug, name, source_kind, source_ref, enabled, archive_enabled, created_at, updated_at;

-- name: DeleteTVChannel :execrows
-- Remove one channel by id. The affected-row count lets the store map an unknown
-- id (0 rows) to ErrTVChannelNotFound without a prior existence query. Its
-- recordings survive: the videos.channel_id FK is ON DELETE SET NULL.
DELETE FROM tv_channels
WHERE id = $1;

-- name: UpsertTVChannelBySlug :one
-- Idempotent seed: insert a channel or, when its slug already exists, refresh
-- only the descriptive fields. enabled and archive_enabled are intentionally NOT
-- overwritten so reseeding never re-arms a channel the operator turned off (or
-- disarmed archiving on); the first insert seeds them from the params.
INSERT INTO tv_channels (slug, name, source_kind, source_ref, enabled, archive_enabled)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (slug) DO UPDATE
    SET name        = EXCLUDED.name,
        source_kind = EXCLUDED.source_kind,
        source_ref  = EXCLUDED.source_ref,
        updated_at  = now()
RETURNING id, slug, name, source_kind, source_ref, enabled, archive_enabled, created_at, updated_at;
