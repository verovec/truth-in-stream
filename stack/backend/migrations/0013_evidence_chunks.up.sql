-- Generalize the evidence corpus. wiki_chunks already held many non-wiki
-- corpora (eurostat, interieur, insee-*, the category crawl) under a text
-- discriminator, so its Wikipedia-shaped identity (page_id/revision_id PK) was
-- a misnomer. evidence_chunks is the single source-extensible evidence store:
-- a new source is rows under a new `source` value, never a migration and never
-- a new column, because source-specific provenance (revision id, section,
-- clustering, and whatever a future source needs) lives in `metadata jsonb`.
-- The typed columns are exactly what queries filter, rank, or join on -
-- `source` (the discriminator the wiki-only reads exclude stat corpora with)
-- and `kind` (the confidence weighter reads it) - so the planner and sqlc keep
-- working; nothing that a query filters or orders by lives in the jsonb.
--
-- Identity is the generic natural key (source, external_id, chunk_index):
-- external_id is text, so a source supplies its own stable id (a Wikipedia
-- page id, a statistical series key) without the column pretending to be a
-- Wikipedia page id. It is part of the key rather than jsonb because upserts
-- conflict on it and every keyset scan orders by it.
--
-- The datastore is greenfield for this work - no production vector data is
-- preserved - so this drops wiki_chunks outright and every corpus re-ingests
-- from source; there is no dual-write or backfill.
--
-- Index choice is the VER-173 benchmark verdict
-- (docs/datastore-scale-benchmark.md): one unpartitioned table with a single
-- halfvec HNSW index (m=16, ef_construction=200). Partition-by-source loses the
-- global evidence search and is not adopted; the binary-quantization index is
-- deferred to VER-176 (opt-in), so it is not created here. The embedding
-- dimension halfvec(1024) is the SQL half of the one-place dimension contract
-- (the Go half is domain.EmbeddingDim); changing it is the model-migration
-- runbook (docs/embedding-model-migration.md), not an ad-hoc edit.
DROP INDEX IF EXISTS wiki_chunks_embedding_hnsw;
DROP TABLE IF EXISTS wiki_chunks;
DROP TABLE IF EXISTS wiki_sync_state;

CREATE TABLE evidence_chunks (
    source      text NOT NULL,
    external_id text NOT NULL,
    chunk_index integer NOT NULL,
    title       text NOT NULL,
    url         text NOT NULL,
    content     text NOT NULL,
    kind        text NOT NULL DEFAULT 'lead',
    embedding   halfvec(1024),
    metadata    jsonb NOT NULL DEFAULT '{}',
    synced_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source, external_id, chunk_index)
);

-- Same HNSW parameters as claims_embedding_hnsw so query-time recall tuning
-- (hnsw.ef_search) behaves identically across both stores. HNSW indexes skip
-- NULL embeddings, so un-embedded chunks (ingest writes rows first, the fleet
-- fills vectors) cost nothing; the bulk-embedding pipeline still loads via a
-- staging table and atomic swap, not through this index.
CREATE INDEX evidence_chunks_embedding_hnsw
    ON evidence_chunks USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 200);

-- Per-source ingestion checkpoint (was wiki_sync_state, keyed on corpus).
-- last_change_ts is the point in time the source is current to; the delta sync
-- resumes from it. Both fields are nullable: a row can exist before the first
-- successful bulk run completes the checkpoint.
CREATE TABLE evidence_sync_state (
    source         text PRIMARY KEY,
    last_change_ts timestamptz,
    dump_version   text,
    synced_at      timestamptz NOT NULL DEFAULT now()
);
