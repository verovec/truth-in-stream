-- Volume control (VER-203), measure 1: an exact-content fingerprint on every
-- evidence chunk, maintained by the database so producers stay simple. A
-- GENERATED STORED column recomputes sha256 of the content bytes on every insert
-- and update, so the hash can never drift from the content it summarizes and no
-- write path has to remember to set it. sha256() is a core builtin (no pgcrypto),
-- and the cast plus the hash are immutable - the two properties a generated
-- expression requires.
--
-- The column lets the single-write ingest workers short-circuit a redundant
-- embed: an unchanged re-crawl finds an identical (source, external_id,
-- chunk_index, content_hash) row already carrying a vector and skips the provider
-- call entirely instead of re-embedding text it already holds. The (source,
-- content_hash) index keeps that lookup - and any future cross-source exact-dup
-- detection - an index probe rather than a scan.
--
-- The staging rebuild path copies this column with LIKE ... INCLUDING GENERATED
-- (see ResetStaging), so a swapped-in corpus keeps the generated column and the
-- hash stays maintained after the atomic swap.
ALTER TABLE evidence_chunks
    ADD COLUMN content_hash bytea
    GENERATED ALWAYS AS (sha256(content::bytea)) STORED;

CREATE INDEX evidence_chunks_source_content_hash
    ON evidence_chunks (source, content_hash);
