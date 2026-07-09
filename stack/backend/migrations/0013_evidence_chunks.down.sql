-- Inverse of 0013: drop the generalized evidence store and restore the
-- Wikipedia-shaped wiki_chunks / wiki_sync_state exactly as they stood after
-- 0004+0009+0010 (page_id/chunk_index PK, revision_id, section, kind,
-- cluster_id, importance columns). Greenfield: neither table carries data
-- across the rollback.
DROP INDEX IF EXISTS evidence_chunks_embedding_hnsw;
DROP TABLE IF EXISTS evidence_chunks;
DROP TABLE IF EXISTS evidence_sync_state;

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
    section     text NOT NULL DEFAULT '',
    kind        text NOT NULL DEFAULT 'lead',
    cluster_id  integer,
    importance  double precision,
    PRIMARY KEY (page_id, chunk_index)
);

CREATE INDEX wiki_chunks_embedding_hnsw
    ON wiki_chunks USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 200);

CREATE TABLE wiki_sync_state (
    corpus         text PRIMARY KEY,
    last_change_ts timestamptz,
    dump_version   text,
    synced_at      timestamptz NOT NULL DEFAULT now()
);
