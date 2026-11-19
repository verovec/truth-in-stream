-- G1: names for the diarized speakers of an analysed video. The stored analysis
-- snapshot (video_analyses.events) is immutable and keeps the diarization
-- letters; this table maps a letter to a person and is applied at read time, so
-- naming never rewrites an analysis.
--
-- One row per (video, label). A row with an empty display_name is a label the
-- naming step left unnamed on purpose. operator_set marks a row a human
-- confirmed or overrode: the naming step replaces only rows where it is false.
CREATE TABLE video_speakers (
    video_id     uuid             NOT NULL REFERENCES videos (id) ON DELETE CASCADE,
    label        text             NOT NULL,
    display_name text             NOT NULL DEFAULT '',
    role         text             NOT NULL DEFAULT 'unknown'
        CHECK (role IN ('principal', 'moderator', 'guest', 'unknown')),
    confidence   double precision NOT NULL DEFAULT 0
        CHECK (confidence >= 0 AND confidence <= 1),
    method       text             NOT NULL DEFAULT 'none'
        CHECK (method IN ('signals', 'tie_break', 'operator', 'none')),
    evidence     jsonb            NOT NULL DEFAULT '[]',
    operator_set boolean          NOT NULL DEFAULT false,
    created_at   timestamptz      NOT NULL DEFAULT now(),
    updated_at   timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (video_id, label)
);
