-- Reverse VER-203's content-hash column and its index. Dropping the column drops
-- the generated expression with it; the corpus rows and embeddings are untouched.
DROP INDEX IF EXISTS evidence_chunks_source_content_hash;
ALTER TABLE evidence_chunks DROP COLUMN IF EXISTS content_hash;
