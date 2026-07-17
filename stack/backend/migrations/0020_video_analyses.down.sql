DROP TABLE video_analyses;
ALTER TABLE videos
    DROP COLUMN analysis_status,
    DROP COLUMN analysis_error,
    DROP COLUMN analyzed_at,
    DROP COLUMN analysis_runs,
    DROP COLUMN analysis_progress_ms;
