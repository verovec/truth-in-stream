-- name: CreateVideo :one
INSERT INTO videos (title, object_key, content_type, size_bytes, status, kind, channel_id, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at;

-- name: GetVideo :one
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at
FROM videos
WHERE id = $1;

-- name: GetVideoByObjectKey :one
-- Resolve a video by its storage object key. The key is UNIQUE, so this is the
-- idempotency probe for a deterministic-key writer: a repeated request for the
-- same recording finds the existing row instead of colliding on the constraint.
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at
FROM videos
WHERE object_key = $1;

-- name: ListTVRecordingsBefore :many
-- Every archived TV recording captured before the cutoff, oldest first, for
-- retention pruning. Scoped to kind 'tv' with a real recorded_at so the scan
-- touches only recordings and the caller need not filter in Go.
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at
FROM videos
WHERE kind = 'tv' AND recorded_at IS NOT NULL AND recorded_at < $1
ORDER BY recorded_at;

-- name: ListTVRecordingsByChannel :many
-- Every ready archived recording for one channel, newest first, for the /tv
-- page's recordings strip. Scoped to kind 'tv' with a ready status so it never
-- returns uploads, imports, or a still-pending capture; an orphaned recording
-- (channel_id nulled by a channel delete) is excluded by the equality match.
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at
FROM videos
WHERE kind = 'tv' AND channel_id = $1 AND status = 'ready' AND recorded_at IS NOT NULL
ORDER BY recorded_at DESC;

-- name: DeleteVideo :execrows
-- Remove one video record by id. The affected-row count lets the store map an
-- unknown id (0 rows) to ErrVideoNotFound without a prior existence query.
DELETE FROM videos
WHERE id = $1;

-- name: ListVideos :many
-- The consumption library (GET /api/videos, the /app grid) excludes kind 'tv':
-- archived channel recordings are surfaced per-channel on /tv, not mixed into the
-- general video library. Individual recordings remain fetchable by id for replay.
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at
FROM videos
WHERE kind <> 'tv'
ORDER BY created_at DESC, id;

-- name: SetVideoStatus :one
UPDATE videos
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at;

-- name: UpsertSampleVideo :one
-- size_bytes keeps a known size against a zero reseed: an offline reseed with no
-- cached media seeds the record with size 0, which must not clobber the real
-- size recorded when the media was last uploaded (the object still exists).
INSERT INTO videos (title, object_key, content_type, size_bytes, status, kind)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (object_key) DO UPDATE
    SET title        = EXCLUDED.title,
        content_type = EXCLUDED.content_type,
        size_bytes   = CASE WHEN EXCLUDED.size_bytes > 0 THEN EXCLUDED.size_bytes ELSE videos.size_bytes END,
        status       = EXCLUDED.status,
        kind         = EXCLUDED.kind,
        updated_at   = now()
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at;

-- name: CreateYouTubeVideo :one
-- Insert a pending YouTube ingest. source_id is the canonical video id; the
-- unique constraint makes a repeat submission a no-op, so DO NOTHING returns no
-- row and the caller resolves the existing record by source id instead.
INSERT INTO videos (title, object_key, content_type, size_bytes, status, kind, source_url, source_id, duration_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (source_id) DO NOTHING
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at;

-- name: GetVideoBySourceID :one
SELECT id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at
FROM videos
WHERE source_id = $1;

-- name: SetVideoReady :one
-- A completed ingest: record the probed title, size, and duration, clear any
-- prior error, and flip the record to ready.
UPDATE videos
SET status = 'ready', title = $2, size_bytes = $3, duration_ms = $4, error = NULL, updated_at = now()
WHERE id = $1
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at;

-- name: SetVideoFailed :one
-- A failed ingest: record the reason and flip the record to failed.
UPDATE videos
SET status = 'failed', error = $2, updated_at = now()
WHERE id = $1
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at;

-- name: RetryFailedVideo :one
-- Atomically claim a failed ingest for retry: flip it back to pending only if it
-- is currently failed, so two concurrent re-submissions cannot both re-download.
-- The guard returns no row (and thus no claim) when the record is not failed.
UPDATE videos
SET status = 'pending', error = NULL, updated_at = now()
WHERE id = $1 AND status = 'failed'
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at;
