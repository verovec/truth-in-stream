-- Inverse of 0004: remove the Wikipedia corpus tables. Nothing else depends
-- on them, so a rollback lands exactly on the 0003 schema.
DROP INDEX IF EXISTS wiki_chunks_embedding_hnsw;
DROP TABLE IF EXISTS wiki_chunks;
DROP TABLE IF EXISTS wiki_sync_state;
