-- Restore the 0018 expression this migration replaced, and drop the wrapper.
ALTER TABLE evidence_chunks DROP COLUMN IF EXISTS content_hash;
ALTER TABLE evidence_chunks
    ADD COLUMN content_hash bytea
    GENERATED ALWAYS AS (sha256(content::bytea)) STORED;

CREATE INDEX evidence_chunks_source_content_hash
    ON evidence_chunks (source, content_hash);

DROP FUNCTION IF EXISTS immutable_sha256(text);
