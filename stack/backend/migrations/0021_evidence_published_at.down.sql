DROP INDEX IF EXISTS political_claims_checked_at;
DROP INDEX IF EXISTS evidence_chunks_source_published_at;
ALTER TABLE evidence_chunks DROP COLUMN IF EXISTS published_at;
