-- name: UpsertWikiChunk :batchexec
-- Ingest never writes embeddings; the CASE keeps an existing embedding only
-- while the content it was computed from is unchanged, so re-ingesting a
-- changed revision invalidates the stale vector instead of serving it.
INSERT INTO wiki_chunks (page_id, chunk_index, title, url, revision_id, corpus, content)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (page_id, chunk_index) DO UPDATE
    SET title = EXCLUDED.title,
        url = EXCLUDED.url,
        revision_id = EXCLUDED.revision_id,
        corpus = EXCLUDED.corpus,
        content = EXCLUDED.content,
        embedding = CASE
            WHEN wiki_chunks.content = EXCLUDED.content THEN wiki_chunks.embedding
            ELSE NULL
        END,
        synced_at = now();

-- name: DeleteWikiPagesByTitle :exec
-- Delta sync removes a hard-deleted page by title: RecentChanges reports a
-- deletion with page id 0, so the stored page can only be found by its title.
DELETE FROM wiki_chunks WHERE title = ANY(sqlc.arg(titles)::text[]);

-- name: StoredWikiRevisions :many
-- Delta sync diffs the revision RecentChanges reports against the one stored, so
-- a page already at that revision is neither refetched nor re-embedded. A page's
-- chunks share a revision after an upsert; max guards against a partial update.
SELECT page_id, max(revision_id)::bigint AS revision_id
FROM wiki_chunks
WHERE page_id = ANY(sqlc.arg(page_ids)::bigint[])
GROUP BY page_id;

-- name: CountWikiPages :one
SELECT count(DISTINCT page_id)::bigint FROM wiki_chunks;

-- name: SetWikiChunkEmbedding :batchexec
-- Delta sync writes embeddings straight into the live table: at delta volume the
-- HNSW index absorbs the inserts incrementally, so no staging swap is needed.
UPDATE wiki_chunks
SET embedding = $1, synced_at = now()
WHERE page_id = $2 AND chunk_index = $3;
-- name: SearchWikiChunks :many
-- Approximate nearest-neighbor retrieval over the embedded corpus, mirroring
-- SearchClaims. The embedding IS NOT NULL filter keeps unembedded chunks out of
-- the result regardless of the chosen plan; the HNSW index only indexes
-- non-null rows, so the filter does not degrade index use. query_embedding is
-- referenced twice but sqlc collapses it to one parameter, so the index still
-- drives the ORDER BY.
SELECT title, url, content, (embedding <=> sqlc.arg(query_embedding))::float8 AS distance
FROM wiki_chunks
WHERE embedding IS NOT NULL
ORDER BY embedding <=> sqlc.arg(query_embedding)
LIMIT sqlc.arg(result_limit);

-- name: DeleteWikiPage :exec
DELETE FROM wiki_chunks WHERE page_id = $1;

-- name: TrimWikiPageChunks :batchexec
-- Removes the stale tail of a page after a re-sync produced fewer chunks
-- (from_index 0 removes the page entirely, e.g. it became a redirect).
DELETE FROM wiki_chunks WHERE page_id = $1 AND chunk_index >= $2;

-- name: AcquireWikiCorpusLock :exec
-- Transaction-scoped advisory lock serializing corpus claims, so two
-- concurrent syncs cannot both pass the foreign-corpus check and claim
-- different corpora. Released automatically at commit/rollback.
SELECT pg_advisory_xact_lock(sqlc.arg(key)::bigint);

-- name: ClaimWikiCorpus :exec
-- Claims the store for a corpus before ingestion starts (and before any
-- checkpoint exists), so a corpus switch is detectable even after a crashed
-- first run. Never overwrites an existing checkpoint.
INSERT INTO wiki_sync_state (corpus) VALUES ($1)
ON CONFLICT (corpus) DO NOTHING;

-- name: GetOtherWikiCorpus :one
SELECT corpus FROM wiki_sync_state WHERE corpus <> $1 LIMIT 1;

-- name: CountWikiChunksForPage :one
SELECT count(*) FROM wiki_chunks WHERE page_id = $1;

-- name: GetWikiChunk :one
SELECT page_id, chunk_index, title, url, revision_id, corpus, content, (embedding IS NULL)::boolean AS embedding_is_null
FROM wiki_chunks
WHERE page_id = $1 AND chunk_index = $2;

-- name: UpsertWikiSyncState :exec
INSERT INTO wiki_sync_state (corpus, last_change_ts, dump_version)
VALUES ($1, $2, $3)
ON CONFLICT (corpus) DO UPDATE
    SET last_change_ts = EXCLUDED.last_change_ts,
        dump_version = EXCLUDED.dump_version,
        synced_at = now();

-- name: GetWikiSyncState :one
SELECT corpus, last_change_ts, dump_version, synced_at
FROM wiki_sync_state
WHERE corpus = $1;

-- name: UnembeddedWikiChunks :many
-- The delta sync reads the chunks it just upserted into the live table back in
-- keyset order to embed them in place. The embedding IS NULL filter scopes the
-- scan to the unembedded chunks a delta run produced.
SELECT page_id, chunk_index, title, url, revision_id, corpus, content
FROM wiki_chunks
WHERE embedding IS NULL
  AND (page_id, chunk_index) > (sqlc.arg(after_page_id)::bigint, sqlc.arg(after_chunk_index)::integer)
ORDER BY page_id, chunk_index
LIMIT sqlc.arg(row_limit);
