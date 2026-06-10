-- Wikipedia corpus: chunked lead sections with revision tracking. Fully
-- separate from the curated claims store. One corpus is ingested per
-- environment (WIKI_CORPUS), so (page_id, chunk_index) identifies a chunk;
-- corpus is recorded for provenance and the per-corpus sync checkpoint.
-- embedding stays nullable here: the bulk-embedding pipeline fills it in a
-- follow-up, and re-ingesting changed content resets it to NULL so stale
-- vectors are never served.
CREATE TABLE wiki_chunks (
    page_id     bigint NOT NULL,
    chunk_index integer NOT NULL,
    title       text NOT NULL,
    url         text NOT NULL,
    revision_id bigint NOT NULL,
    corpus      text NOT NULL,
    content     text NOT NULL,
    embedding   halfvec(1024),
    synced_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, chunk_index)
);

-- Same HNSW parameters as claims_embedding_hnsw so query-time recall tuning
-- (hnsw.ef_search) behaves identically across both stores. Plain CREATE INDEX
-- is correct because the table is empty at creation; HNSW indexes skip NULL
-- embeddings, so unembedded chunks cost nothing. The bulk-embedding card
-- loads via a staging table and atomic swap, not through this index.
CREATE INDEX wiki_chunks_embedding_hnsw
    ON wiki_chunks USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 200);

-- Per-corpus ingestion checkpoint. last_change_ts is the point in time the
-- corpus is current to; the delta-sync card resumes from it. Both fields are
-- nullable: a row can exist before the first successful bulk run completes
-- the checkpoint.
CREATE TABLE wiki_sync_state (
    corpus         text PRIMARY KEY,
    last_change_ts timestamptz,
    dump_version   text,
    synced_at      timestamptz NOT NULL DEFAULT now()
);
