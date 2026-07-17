-- name: UpsertVideoAnalysis :one
-- Persist a completed run's full event stream, one row per video: a
-- re-analysis overwrites the previous result atomically (the run counter on
-- videos, not rows here, is the history).
INSERT INTO video_analyses (video_id, snapshot_version, events, engine, claims_total, claims_credible, claims_disputed, claims_unverifiable)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (video_id) DO UPDATE
    SET snapshot_version    = EXCLUDED.snapshot_version,
        events              = EXCLUDED.events,
        engine              = EXCLUDED.engine,
        claims_total        = EXCLUDED.claims_total,
        claims_credible     = EXCLUDED.claims_credible,
        claims_disputed     = EXCLUDED.claims_disputed,
        claims_unverifiable = EXCLUDED.claims_unverifiable,
        updated_at          = now()
RETURNING video_id, snapshot_version, events, engine, claims_total, claims_credible, claims_disputed, claims_unverifiable, created_at, updated_at;

-- name: GetVideoAnalysis :one
SELECT video_id, snapshot_version, events, engine, claims_total, claims_credible, claims_disputed, claims_unverifiable, created_at, updated_at
FROM video_analyses
WHERE video_id = $1;

-- name: LockVideoForAnalysis :one
-- Claim a ready video for a fresh analysis run: flip it to analysing (the
-- lock), zero the progress position, and clear any prior error - all in one
-- guarded update. The guard admits a video that is ready and not already
-- analysing (so a none/complete/failed analysis re-runs, a concurrent run is
-- excluded). No row returned means the store resolves why (unknown, not
-- ready, or already analysing) and maps it to the right error. The previous
-- stored analysis is NOT wiped here: it stays readable until the new run
-- completes and overwrites it.
UPDATE videos
SET analysis_status = 'analysing', analysis_progress_ms = 0, analysis_error = '', updated_at = now()
WHERE id = $1 AND status = 'ready' AND analysis_status <> 'analysing'
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at, analysis_status, analysis_error, analyzed_at, analysis_runs, analysis_progress_ms;

-- name: SetVideoAnalysisProgress :exec
-- Advance the run's audio position. Progress is database state, so it
-- survives a refresh and a restart.
UPDATE videos
SET analysis_progress_ms = $2, updated_at = now()
WHERE id = $1;

-- name: CompleteVideoAnalysisStatus :one
-- Terminal success: mark the run complete, stamp the completion time, and
-- count the run. Paired with UpsertVideoAnalysis in one transaction so the
-- status flip and the stored result are atomic.
UPDATE videos
SET analysis_status = 'complete', analysis_error = '', analyzed_at = now(), analysis_runs = analysis_runs + 1, updated_at = now()
WHERE id = $1
RETURNING id, title, object_key, content_type, size_bytes, status, kind, created_at, updated_at, source_url, source_id, duration_ms, error, channel_id, recorded_at, analysis_status, analysis_error, analyzed_at, analysis_runs, analysis_progress_ms;

-- name: FailVideoAnalysis :exec
-- Terminal failure: record the reason and flip the run to failed so the
-- operator can re-analyse. A previously stored analysis is untouched and
-- stays readable.
UPDATE videos
SET analysis_status = 'failed', analysis_error = $2, updated_at = now()
WHERE id = $1;

-- name: RecoverInterruptedVideoAnalyses :many
-- Startup recovery: any video left analysing when the process died is flipped
-- to failed with a clear reason. Returns the recovered ids for logging.
UPDATE videos
SET analysis_status = 'failed', analysis_error = 'interrupted by restart', updated_at = now()
WHERE analysis_status = 'analysing'
RETURNING id;
