-- Inverse of 0017: drop the lexical full-text branch and land back on the
-- pre-0017 schema. Columns are dropped before the function they depend on. The
-- unaccent extension is left in place: it is cheap, harmless, and may back other
-- objects, and removing it is not required to restore the schema shape (the up
-- migration creates it with IF NOT EXISTS, so a re-apply is idempotent).
DROP INDEX IF EXISTS evidence_chunks_search_vector_gin;
ALTER TABLE evidence_chunks DROP COLUMN IF EXISTS search_vector;

DROP INDEX IF EXISTS claims_search_vector_gin;
ALTER TABLE claims DROP COLUMN IF EXISTS search_vector;

DROP FUNCTION IF EXISTS immutable_unaccent(text);
