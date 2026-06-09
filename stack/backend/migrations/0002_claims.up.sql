-- Supersede the placeholder documents table from 0001 with the real claims
-- schema. documents was skeleton scaffolding and never held real data, so the
-- drop is safe; this migration is append-only so existing local databases
-- upgrade cleanly rather than requiring an edit to an already-applied migration.
DROP INDEX IF EXISTS documents_embedding_hnsw;
DROP TABLE IF EXISTS documents;

-- A curated, verified claim and the embedding used to match spoken segments
-- against it. verdict is constrained to the three values the matching step
-- understands. sources is a JSON array of {title, url} objects.
CREATE TABLE claims (
    id        text PRIMARY KEY,
    content   text NOT NULL,
    verdict   text NOT NULL CHECK (verdict IN ('corroborates', 'contradicts', 'unclear')),
    sources   jsonb NOT NULL DEFAULT '[]',
    embedding halfvec(1024) NOT NULL
);

-- HNSW index for cosine similarity (matches the <=> operator used by Search).
-- Plain CREATE INDEX is correct here because the table is empty at creation.
-- Any later index added to the populated table must use CREATE INDEX
-- CONCURRENTLY (outside a transaction) to avoid blocking writes during the
-- build. At scale, raise maintenance_work_mem and
-- max_parallel_maintenance_workers in the RDS parameter group before
-- bulk-loading so the build stays in memory.
CREATE INDEX claims_embedding_hnsw
    ON claims USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 200);
