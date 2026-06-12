---
name: data-map
description: Use when a prompt concerns the data structure - which table or column holds a given fact, where an ingested asset lives, or how to query the database. The data dictionary for the Postgres + pgvector store.
---

# Data Map

The data dictionary for the `truthinstream` Postgres + pgvector database. Read this whenever a
prompt is about where data lives, what a column means, or how to query the store, so you target
the right table instead of guessing.

## Ground truth and anti-drift

This file is a map, not the source. The authoritative schema is the migrations; the authoritative
access patterns are the sqlc queries.

- Schema: `stack/backend/migrations/*.up.sql` (applied in numeric order).
- Queries: `stack/backend/queries/{claims,wiki,videos}.sql`.

- You MUST verify a column, type, or constraint against those files (or the live database via the
  read-only `postgres` MCP) before depending on it. NEVER assert a column exists from this doc
  alone if the answer drives a code change or a migration.
- When you change the schema in a migration, you MUST update this file in the same change. A stale
  data map is a defect.

## Querying the database

- A read-only `postgres` MCP server is wired in `.mcp.json` (Crystal DBA `postgres-mcp`,
  `--access-mode=restricted`) pointed at the local dev DB
  (`postgresql://postgres:dev@localhost:5432/truthinstream`). Use it to introspect schema and run
  `SELECT`s when answering data-structure questions.
- It is READ-ONLY by design. NEVER attempt inserts, updates, deletes, or DDL through it; schema
  changes go through a migration, not the MCP.
- The MCP needs the dev stack up (`docker compose up` / `make` targets) so port 5432 is live.

## Live tables

Four tables hold real data. Each `embedding` is `halfvec(1024)` from Voyage `voyage-4-large`
(1024 dims, HNSW cosine, same model for ingest `input_type=document` and query `input_type=query`).

### `claims` - curated verified claims (vector store)

The matching corpus: spoken segments are matched against these by cosine similarity.

| Column | Type | Meaning |
|--------|------|---------|
| `id` | `text` PK | Stable claim id (upsert key). |
| `content` | `text` | The claim text. |
| `verdict` | `text` | CHECK in `('corroborates','contradicts','unclear')` - the only values matching understands. |
| `sources` | `jsonb` | Array of `{title, url}` objects, default `[]`. |
| `embedding` | `halfvec(1024)` NOT NULL | Claim vector. |

- Index `claims_embedding_hnsw` (HNSW `halfvec_cosine_ops`, `m=16`, `ef_construction=200`).
- Retrieval: `SearchClaims` orders by `embedding <=> query_embedding` (cosine distance).
- This table decides coverage / checkability. If a question is "is this segment checkable / covered",
  the answer comes from `claims`, NOT from `wiki_chunks`.

### `wiki_chunks` - Wikipedia evidence corpus (vector store)

Chunked Wikipedia lead sections, bulk-ingested. Evidence surfaced for matched claims.

| Column | Type | Meaning |
|--------|------|---------|
| `page_id` | `bigint` | Wikipedia page id. Part of PK. |
| `chunk_index` | `integer` | Chunk ordinal within the page. Part of PK. |
| `title` | `text` | Article title. |
| `url` | `text` | Article URL. |
| `revision_id` | `bigint` | Source revision; delta sync diffs against it. |
| `corpus` | `text` | Which corpus this chunk belongs to (`WIKI_CORPUS`); provenance + checkpoint key. |
| `content` | `text` | Chunk text. |
| `embedding` | `halfvec(1024)` NULLABLE | Filled by the embedding pipeline. NULL = unembedded. |
| `synced_at` | `timestamptz` | Last ingest/embed time. |
| `section` | `text` default `''` | Article section a chunk came from; lead has `''`. |
| `kind` | `text` default `'lead'` | `'lead'` today; `'body'` reserved for later. |

- PK `(page_id, chunk_index)`. Index `wiki_chunks_embedding_hnsw` (same HNSW params; skips NULLs).
- `embedding` is NULLABLE on purpose: ingest never writes it, and re-ingesting changed `content`
  resets it to NULL so a stale vector is never served (`UpsertWikiChunk` CASE). `SearchWikiChunks`
  filters `WHERE embedding IS NOT NULL`.
- Evidence-only. It enriches matched claims; it does NOT decide coverage.

### `wiki_sync_state` - per-corpus ingestion checkpoint

| Column | Type | Meaning |
|--------|------|---------|
| `corpus` | `text` PK | Corpus name. |
| `last_change_ts` | `timestamptz` NULL | Point the corpus is current to; delta sync resumes here. |
| `dump_version` | `text` NULL | Dump the bulk load came from. |
| `synced_at` | `timestamptz` | Last sync time. |

### `videos` - video catalog (no embeddings)

A durable record per playable clip: uploads, curated samples, and YouTube ingests. Separate from
the vector stores.

| Column | Type | Meaning |
|--------|------|---------|
| `id` | `uuid` PK `gen_random_uuid()` | Video identity. |
| `title` | `text` | Display title. |
| `object_key` | `text` UNIQUE | Storage object key; uniqueness makes sample seeding idempotent. |
| `content_type` | `text` | MIME type. |
| `size_bytes` | `bigint` | Object size. |
| `status` | `text` | Lifecycle (e.g. `pending` -> `ready`). |
| `kind` | `text` | Source kind (sample / upload / youtube). |
| `created_at` / `updated_at` | `timestamptz` | Timestamps. |
| `source_url` | `text` NULL | YouTube watch URL (NULL for non-YouTube). |
| `source_id` | `text` UNIQUE NULL | YouTube 11-char id; re-submitting the same link is a no-op. |
| `duration_ms` | `bigint` default `0` | Probed length. |
| `error` | `text` NULL | Why an ingest failed. |

- Index `videos_created_at_idx (created_at DESC)` - the library lists newest first.

## Tables that no longer exist

Do not look for these; they were dropped and have no reader or writer.

- `documents` - skeleton from `0001`, superseded by `claims` in `0002`.
- `segment_results`, `processed_videos` - batch fact-check tables, dropped in `0008`. Imported
  videos now stream live exactly like a live stream: the live path emits verdicts over the
  WebSocket and persists nothing. There is no stored per-segment verdict to query.

## Cross-cutting facts (known landmines)

- Bulk vector loads MUST use text-format CSV `COPY`, never binary `CopyFrom`/binary `COPY`: pgx
  binary copy corrupts `halfvec` columns (phantom rows). `pg_dump`/`pg_restore` are safe (text I/O).
- Coverage / checkability is decided by `claims`; `wiki_chunks` is evidence-only. Populating the
  wiki corpus cannot change whether a segment is covered.
- Live fact-check results are ephemeral - streamed over the WebSocket, not stored. If asked "where
  are the verdicts for video X", the answer is "nowhere; they are emitted live, not persisted".
- All vectors are `halfvec(1024)` / Voyage `voyage-4-large`. A mismatch in dims or model is a bug,
  not a config choice.
