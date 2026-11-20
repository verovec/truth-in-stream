-- Durable video analysis storage (VER-216). The videos table gains the
-- analysis lifecycle mirrored from documents ('analysing' doubles as the job
-- lock for the pre-analysis runner); video_analyses persists a completed run's
-- full ordered event stream so playback and exports outlive the 24 h Redis
-- replay cache.
ALTER TABLE videos
    ADD COLUMN analysis_status text NOT NULL DEFAULT 'none'
        CHECK (analysis_status IN ('none', 'analysing', 'complete', 'failed')),
    ADD COLUMN analysis_error text NOT NULL DEFAULT '',
    ADD COLUMN analyzed_at timestamptz,
    ADD COLUMN analysis_runs int NOT NULL DEFAULT 0,
    -- Audio position the run has reached, persisted so progress survives a
    -- refresh and a restart.
    ADD COLUMN analysis_progress_ms bigint NOT NULL DEFAULT 0;

-- One row per video; a re-analysis overwrites it (history is the
-- analysis_runs counter, not rows). events holds the ordered live events the
-- pipeline emitted, with absolute video-time timestamps, in the same JSON
-- shape the Redis snapshot stores; snapshot_version guards decoding across
-- schema bumps. engine records the model identifiers and config fingerprint of
-- the run so the operator can see what produced a result before re-analysing.
-- The claim counters are denormalized for list badges without loading the
-- blob.
CREATE TABLE video_analyses (
    video_id            uuid PRIMARY KEY REFERENCES videos (id) ON DELETE CASCADE,
    snapshot_version    int NOT NULL,
    events              jsonb NOT NULL,
    engine              jsonb NOT NULL,
    claims_total        int NOT NULL,
    claims_credible     int NOT NULL,
    claims_disputed     int NOT NULL,
    claims_unverifiable int NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
