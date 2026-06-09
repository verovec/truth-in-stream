CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE documents (
    id        text PRIMARY KEY,
    content   text NOT NULL,
    metadata  jsonb NOT NULL DEFAULT '{}',
    embedding halfvec(1024) NOT NULL
);

-- HNSW index for cosine similarity (matches the <=> operator used by Search).
-- Plain CREATE INDEX is correct here because the table is empty at creation.
-- Any later index added to the populated table must use CREATE INDEX
-- CONCURRENTLY (outside a transaction) to avoid blocking writes during the
-- build. At scale, raise maintenance_work_mem and
-- max_parallel_maintenance_workers in the RDS parameter group before
-- bulk-loading so the build stays in memory.
CREATE INDEX documents_embedding_hnsw
    ON documents USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 200);
