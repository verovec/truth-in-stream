-- YouTube ingest extends the video catalog with the columns a server-side
-- download needs. source_url is the operator-submitted watch URL; source_id is
-- YouTube's canonical 11-character video id and is UNIQUE so re-submitting the
-- same link is a no-op. NULLs are distinct in a Postgres unique index, so the
-- existing uploads and samples (source_id NULL) coexist freely. duration_ms
-- records the probed length the playlist displays; error captures why an ingest
-- failed so a failed item can show its reason.
ALTER TABLE videos ADD COLUMN source_url  text;
ALTER TABLE videos ADD COLUMN source_id   text UNIQUE;
ALTER TABLE videos ADD COLUMN duration_ms bigint NOT NULL DEFAULT 0;
ALTER TABLE videos ADD COLUMN error       text;
