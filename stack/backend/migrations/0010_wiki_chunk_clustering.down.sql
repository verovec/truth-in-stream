ALTER TABLE wiki_chunks
    DROP COLUMN IF EXISTS importance,
    DROP COLUMN IF EXISTS cluster_id;
