-- Inverse of 0002: drop claims and restore the 0001 placeholder documents
-- table so a rollback lands exactly on the 0001 schema. The extension guard
-- keeps this down migration self-contained: restoring documents needs the
-- halfvec type even if the extension was dropped by an earlier rollback.
CREATE EXTENSION IF NOT EXISTS vector;
DROP INDEX IF EXISTS claims_embedding_hnsw;
DROP TABLE IF EXISTS claims;

CREATE TABLE documents (
    id        text PRIMARY KEY,
    content   text NOT NULL,
    metadata  jsonb NOT NULL DEFAULT '{}',
    embedding halfvec(1024) NOT NULL
);

CREATE INDEX documents_embedding_hnsw
    ON documents USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 200);
