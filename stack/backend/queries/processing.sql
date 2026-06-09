-- name: UpsertSegmentResult :exec
INSERT INTO segment_results (video_id, start_ms, end_ms, content, matches)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (video_id, start_ms) DO UPDATE
    SET end_ms  = EXCLUDED.end_ms,
        content = EXCLUDED.content,
        matches = EXCLUDED.matches;

-- name: DeleteSegmentResults :exec
DELETE FROM segment_results
WHERE video_id = $1;

-- name: MarkVideoProcessed :exec
INSERT INTO processed_videos (video_id, segment_count)
VALUES ($1, $2)
ON CONFLICT (video_id) DO UPDATE
    SET segment_count = EXCLUDED.segment_count,
        completed_at  = now();

-- name: GetProcessedVideoSegmentCount :one
SELECT segment_count
FROM processed_videos
WHERE video_id = $1;

-- name: ListSegmentResults :many
SELECT video_id, start_ms, end_ms, content, matches
FROM segment_results
WHERE video_id = $1
ORDER BY start_ms;
