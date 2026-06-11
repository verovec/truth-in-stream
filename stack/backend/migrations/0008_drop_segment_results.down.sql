-- Recreate the batch fact-check result tables (inverse of 0008), restoring the
-- schema as it stood after 0003 and 0005: the per-segment results keyed by
-- (video_id, start_ms) including the skip_reason column, plus the completion
-- markers.
CREATE TABLE segment_results (
    video_id    text NOT NULL,
    start_ms    bigint NOT NULL,
    end_ms      bigint NOT NULL,
    content     text NOT NULL,
    matches     jsonb NOT NULL DEFAULT '[]',
    skip_reason text NOT NULL DEFAULT '',
    PRIMARY KEY (video_id, start_ms)
);

CREATE TABLE processed_videos (
    video_id      text PRIMARY KEY,
    segment_count integer NOT NULL,
    completed_at  timestamptz NOT NULL DEFAULT now()
);
