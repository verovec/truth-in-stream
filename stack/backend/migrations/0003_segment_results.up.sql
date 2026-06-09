-- Per-segment fact-check results for a processed video, keyed by
-- (video_id, start_ms). video_id is the SHA-256 hex digest of the video
-- source, so reprocessing the same source upserts in place. Timestamps are
-- milliseconds from video start. matches holds the ranked claim matches in
-- the stable client shape: [{claim, verdict, sources, similarity}].
CREATE TABLE segment_results (
    video_id text NOT NULL,
    start_ms bigint NOT NULL,
    end_ms   bigint NOT NULL,
    content  text NOT NULL,
    matches  jsonb NOT NULL DEFAULT '[]',
    PRIMARY KEY (video_id, start_ms)
);

-- Completion markers. A video appears here only after every one of its
-- segment results has been persisted, so presence is what makes cached
-- results servable - partial runs are never presented as complete.
CREATE TABLE processed_videos (
    video_id      text PRIMARY KEY,
    segment_count integer NOT NULL,
    completed_at  timestamptz NOT NULL DEFAULT now()
);
