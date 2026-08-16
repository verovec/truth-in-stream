-- VER-227: evidence publication dates.
--
-- evidence_chunks gains the real-world publication date of the passage as a
-- typed column, following the schema rule that anything a query filters or
-- orders by lives in a typed column, never in the metadata jsonb. NULL means
-- the source is genuinely undated (encyclopedic content), not unknown-recent;
-- ingestion writes a date only when the source exposes one, never a guess.
--
-- The partial indexes serve the two read patterns this enables: recency-ordered
-- corpus queries per source, and the curated fast-borrow age guard on
-- political_claims.checked_at (the column has existed since 0011 but nothing
-- indexed or read it). Partial because undated rows can never match a dated
-- query, so indexing them buys nothing.

ALTER TABLE evidence_chunks
    ADD COLUMN published_at timestamptz;

CREATE INDEX evidence_chunks_source_published_at
    ON evidence_chunks (source, published_at DESC)
    WHERE published_at IS NOT NULL;

CREATE INDEX political_claims_checked_at
    ON political_claims (checked_at DESC)
    WHERE checked_at IS NOT NULL;
