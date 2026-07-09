-- name: UpsertEvidenceChunk :batchexec
-- Ingest never writes embeddings; the CASE keeps an existing embedding only
-- while the content it was computed from is unchanged, so re-ingesting a
-- changed revision invalidates the stale vector instead of serving it. metadata
-- MERGES (`existing || new`) rather than replacing, so the ingest keys the
-- writer supplies (revision id, section) overwrite while the offline clustering
-- keys (cluster_id, importance) the ingest does not carry survive a re-ingest -
-- the old schema kept importance in a dedicated column the upsert never touched,
-- and UnembeddedLive reads that importance to embed the most important content
-- first.
INSERT INTO evidence_chunks (source, external_id, chunk_index, title, url, content, kind, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (source, external_id, chunk_index) DO UPDATE
    SET title = EXCLUDED.title,
        url = EXCLUDED.url,
        content = EXCLUDED.content,
        kind = EXCLUDED.kind,
        metadata = evidence_chunks.metadata || EXCLUDED.metadata,
        embedding = CASE
            WHEN evidence_chunks.content = EXCLUDED.content THEN evidence_chunks.embedding
            ELSE NULL
        END,
        synced_at = now();

-- name: UpsertEmbeddedEvidenceChunk :exec
-- Crawl ingestion writes content and embedding together: the worker embeds the
-- self-contained message, then upserts the whole row in one statement so a chunk
-- is never visible to search without its matching vector. The embedding is always
-- the freshly computed one, so a re-crawl rewrites the same vector idempotently.
INSERT INTO evidence_chunks (source, external_id, chunk_index, title, url, content, kind, metadata, embedding, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
ON CONFLICT (source, external_id, chunk_index) DO UPDATE
    SET title = EXCLUDED.title,
        url = EXCLUDED.url,
        content = EXCLUDED.content,
        kind = EXCLUDED.kind,
        metadata = evidence_chunks.metadata || EXCLUDED.metadata,
        embedding = EXCLUDED.embedding,
        synced_at = now();

-- name: DeleteEvidenceByTitle :exec
-- Delta sync removes a hard-deleted page by title within its own source:
-- RecentChanges reports a deletion with page id 0, so the stored chunk can only
-- be found by its title. Scoping to the source keeps a same-titled chunk of
-- another source safe.
DELETE FROM evidence_chunks
WHERE source = sqlc.arg(source) AND title = ANY(sqlc.arg(titles)::text[]);

-- name: StoredEvidenceRevisions :many
-- Delta sync diffs the revision RecentChanges reports against the one stored, so
-- a document already at that revision is neither refetched nor re-embedded. A
-- document's chunks share a revision after an upsert; max guards a partial
-- update. revision_id lives in the metadata jsonb now (a wiki-source key), so a
-- row missing the key yields NULL from the ->> and COALESCE floors it to 0 - a
-- non-numeric revision the delta always treats as stale and refetches, rather
-- than a NULL that would fail the non-nullable int64 scan and stall the sync.
SELECT external_id, COALESCE(max((metadata->>'revision_id')::bigint), 0)::bigint AS revision_id
FROM evidence_chunks
WHERE source = sqlc.arg(source) AND external_id = ANY(sqlc.arg(external_ids)::text[])
GROUP BY external_id;

-- name: CountEvidenceDocuments :one
-- The delta-sync bulk-recommendation denominator counts only the encyclopedic
-- corpora: every statistical source (separate sources that share this table) is
-- excluded so its rows never skew the change-fraction guard. A document is one
-- (source, external_id).
SELECT count(DISTINCT (source, external_id))::bigint
FROM evidence_chunks
WHERE source <> ALL(sqlc.arg(exclude_sources)::text[]);

-- name: SetEvidenceChunkEmbedding :batchexec
-- Delta sync writes embeddings straight into the live table: at delta volume the
-- HNSW index absorbs the inserts incrementally, so no staging swap is needed.
UPDATE evidence_chunks
SET embedding = $1, synced_at = now()
WHERE source = $2 AND external_id = $3 AND chunk_index = $4;

-- name: SearchEvidenceChunks :many
-- Approximate nearest-neighbor retrieval over the embedded corpus, mirroring
-- SearchClaims. The embedding IS NOT NULL filter keeps unembedded chunks out of
-- the result regardless of the chosen plan; the HNSW index only indexes
-- non-null rows, so the filter does not degrade index use. query_embedding is
-- referenced twice but sqlc collapses it to one parameter, so the index still
-- drives the ORDER BY.
SELECT source, external_id, chunk_index, title, url, content, kind, metadata,
       (embedding <=> sqlc.arg(query_embedding))::float8 AS distance
FROM evidence_chunks
WHERE embedding IS NOT NULL
ORDER BY embedding <=> sqlc.arg(query_embedding)
LIMIT sqlc.arg(result_limit);

-- name: DeleteEvidenceDocument :exec
DELETE FROM evidence_chunks WHERE source = $1 AND external_id = $2;

-- name: TrimEvidenceDocumentChunks :batchexec
-- Removes the stale tail of a document after a re-sync produced fewer chunks
-- (from_index 0 removes the document entirely, e.g. it became a redirect).
DELETE FROM evidence_chunks WHERE source = $1 AND external_id = $2 AND chunk_index >= $3;

-- name: AcquireEvidenceSourceLock :exec
-- Transaction-scoped advisory lock serializing source claims, so two concurrent
-- syncs cannot both pass the foreign-source check and claim different sources.
-- Released automatically at commit/rollback.
SELECT pg_advisory_xact_lock(sqlc.arg(key)::bigint);

-- name: ClaimEvidenceSource :exec
-- Claims the store for a source before ingestion starts (and before any
-- checkpoint exists), so a source switch is detectable even after a crashed
-- first run. Never overwrites an existing checkpoint.
INSERT INTO evidence_sync_state (source) VALUES ($1)
ON CONFLICT (source) DO NOTHING;

-- name: GetOtherEvidenceSource :one
SELECT source FROM evidence_sync_state WHERE source <> $1 LIMIT 1;

-- name: CountEvidenceChunksForDocument :one
SELECT count(*) FROM evidence_chunks WHERE source = $1 AND external_id = $2;

-- name: GetEvidenceChunk :one
SELECT source, external_id, chunk_index, title, url, content, kind, metadata,
       (embedding IS NULL)::boolean AS embedding_is_null
FROM evidence_chunks
WHERE source = $1 AND external_id = $2 AND chunk_index = $3;

-- name: UpsertEvidenceSyncState :exec
INSERT INTO evidence_sync_state (source, last_change_ts, dump_version)
VALUES ($1, $2, $3)
ON CONFLICT (source) DO UPDATE
    SET last_change_ts = EXCLUDED.last_change_ts,
        dump_version = EXCLUDED.dump_version,
        synced_at = now();

-- name: GetEvidenceSyncState :one
SELECT source, last_change_ts, dump_version, synced_at
FROM evidence_sync_state
WHERE source = $1;

-- name: UnembeddedEvidenceChunks :many
-- The delta sync reads the chunks it just upserted into the live table back in
-- keyset order to embed them in place. The embedding IS NULL filter scopes the
-- scan to the unembedded chunks a delta run produced. The keyset spans the full
-- (source, external_id, chunk_index) because external_id is unique only within a
-- source.
SELECT source, external_id, chunk_index, title, url, content, kind, metadata
FROM evidence_chunks
WHERE embedding IS NULL
  AND (source, external_id, chunk_index) > (sqlc.arg(after_source)::text, sqlc.arg(after_external_id)::text, sqlc.arg(after_chunk_index)::integer)
ORDER BY source, external_id, chunk_index
LIMIT sqlc.arg(row_limit);

-- name: EmbeddedEvidenceChunks :many
-- The clustering job reads the embedded live corpus in keyset order to group it
-- into topic clusters and score importance. The embedding IS NOT NULL filter
-- scopes the scan to the chunks that actually carry a vector to cluster, and the
-- source filter keeps statistical evidence (separate sources sharing this table)
-- out of the encyclopedic clustering it does not belong to.
SELECT source, external_id, chunk_index, embedding
FROM evidence_chunks
WHERE embedding IS NOT NULL
  AND source <> ALL(sqlc.arg(exclude_sources)::text[])
  AND (source, external_id, chunk_index) > (sqlc.arg(after_source)::text, sqlc.arg(after_external_id)::text, sqlc.arg(after_chunk_index)::integer)
ORDER BY source, external_id, chunk_index
LIMIT sqlc.arg(row_limit);

-- name: SetEvidenceChunkClustering :batchexec
-- The clustering job writes each chunk's cluster id and importance into the
-- metadata jsonb, merging with the || operator so the revision id and section
-- keys survive. The casts pin the params to plain integer/double so a non-null
-- write is a value, not a nullable pointer.
UPDATE evidence_chunks
SET metadata = metadata || jsonb_build_object(
        'cluster_id', sqlc.arg(cluster_id)::integer,
        'importance', sqlc.arg(importance)::double precision)
WHERE source = sqlc.arg(source)
  AND external_id = sqlc.arg(external_id)
  AND chunk_index = sqlc.arg(chunk_index)::integer;
