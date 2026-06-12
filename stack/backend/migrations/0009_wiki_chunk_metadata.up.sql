-- Per-chunk classification metadata for the wiki corpus: the article section a
-- chunk was extracted from (the lead section has no heading, so '') and a coarse
-- kind ('lead' vs a future 'body'). Both columns are additive, NOT NULL, and
-- defaulted, so existing rows backfill to the defaults without a migration step
-- and the staging table created at runtime with
-- `CREATE TABLE wiki_chunks_staging (LIKE wiki_chunks INCLUDING DEFAULTS)` gains
-- the same columns and defaults, keeping the atomic staging-to-live swap
-- symmetric. Ingestion extracts only lead sections today, so every new chunk is
-- kind 'lead'; the columns exist for later body extraction and confidence
-- weighting. Kept as plain text (no CHECK) so LIKE INCLUDING DEFAULTS yields a
-- byte-identical staging table; the kind vocabulary is enforced in the domain.
ALTER TABLE wiki_chunks
    ADD COLUMN section text NOT NULL DEFAULT '',
    ADD COLUMN kind    text NOT NULL DEFAULT 'lead';
