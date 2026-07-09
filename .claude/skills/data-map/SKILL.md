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
- Queries: `stack/backend/queries/{claims,evidence,political,videos}.sql`.

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

Six tables hold real data. Each `embedding` is `halfvec(1024)` from Voyage `voyage-4-large`
(1024 dims, HNSW cosine, same model for ingest `input_type=document` and query `input_type=query`).
The political-fact-check stores (`political_claims`, `voting_records`) are additive and inert until
the political verify path (`FACTCHECK_POLITICAL`, later in epic VER-93) wires them in.

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
  the answer comes from `claims`, NOT from `evidence_chunks`.

### `evidence_chunks` - generalized evidence corpus (vector store)

The source-extensible evidence store (VER-174; was `wiki_chunks`). Holds every evidence source under
one `source` discriminator: chunked Wikipedia lead sections, the category crawl, and the statistical
sources (`eurostat`, `interieur`, `insee-*`). Evidence surfaced for matched claims. **Adding a source
is rows under a new `source` value - no migration and no new column** - because source-specific
provenance lives in `metadata jsonb`.

| Column | Type | Meaning |
|--------|------|---------|
| `source` | `text` | The source discriminator (a Wikipedia corpus name, a statistical source); the wiki-only reads exclude the stat sources by it. Part of PK. |
| `external_id` | `text` | The source's stable document id (a Wikipedia page id, a statistical series key), as text. Part of PK. |
| `chunk_index` | `integer` | Chunk ordinal within the document. Part of PK. |
| `title` | `text` | Document title. |
| `url` | `text` | Document URL. |
| `content` | `text` | Chunk text. |
| `kind` | `text` default `'lead'` | `'lead'` or `'body'`; the confidence weighter reads it, so it stays typed. |
| `embedding` | `halfvec(1024)` NULLABLE | Filled by the embedding pipeline. NULL = unembedded. |
| `metadata` | `jsonb` NOT NULL default `'{}'` | Source-specific provenance. Wiki/crawl keys: `revision_id`, `section`, `cluster_id`, `importance` (see `domain.WikiMetadata`). A stats chunk carries `{}`. **Nothing a query filters or ranks on lives here** - those are the typed columns above. |
| `synced_at` | `timestamptz` | Last ingest/embed time. |

- PK `(source, external_id, chunk_index)` - the generic natural key; including `source` lets two
  sources share an `external_id` without colliding. Index `evidence_chunks_embedding_hnsw` (HNSW
  `halfvec_cosine_ops`, `m=16`, `ef_construction=200`; skips NULLs). Single unpartitioned index per
  the VER-173 benchmark verdict (`docs/datastore-scale-benchmark.md`).
- A second index `evidence_chunks_embedding_bit_hnsw` (HNSW over `binary_quantize(embedding)::bit(1024)`
  with `bit_hamming_ops`, VER-176) backs the OPT-IN two-stage binary-quantization search: it is a
  ~6x smaller RAM working set that gathers coarse candidates by Hamming distance before a halfvec
  rerank. Off by default (`EVIDENCE_BQ_MULTIPLIER=0`); the single-stage halfvec search is the
  default, and the bit index just sits unused until an operator enables the two-stage path
  (`postgres.WithBinaryQuantization`, wired from `EVIDENCE_BQ_MULTIPLIER` in `cmd/server`).
- `embedding` is NULLABLE on purpose: ingest never writes it, and re-ingesting changed `content`
  resets it to NULL so a stale vector is never served (`UpsertEvidenceChunk` CASE).
  `SearchEvidenceChunks` filters `WHERE embedding IS NOT NULL`.
- The dimension has one Go source of truth (`domain.EmbeddingDim`, surfaced as
  `domain.HalfvecColumnType()`) and one SQL declaration (the `halfvec(1024)` migration DDL); changing
  it is `docs/embedding-model-migration.md`, not an ad-hoc edit.
- Evidence-only. It enriches matched claims; it does NOT decide coverage.

### `evidence_sync_state` - per-source ingestion checkpoint

| Column | Type | Meaning |
|--------|------|---------|
| `source` | `text` PK | Source name (was `corpus`). |
| `last_change_ts` | `timestamptz` NULL | Point the source is current to; delta sync resumes here. |
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

### `political_claims` - curated pre-checked political claims (vector store)

The political fast-path matcher: spoken segments are matched against these by cosine similarity to
borrow an instant two-axis verdict for a repeated talking point. Separate from `claims` (which uses
the corroborates/contradicts verdict model); this store carries the literal-plus-flags model.

| Column | Type | Meaning |
|--------|------|---------|
| `id` | `text` PK | Stable claim id (upsert key). |
| `content` | `text` | The claim text. |
| `literal_verdict` | `text` | CHECK in `('accurate','inaccurate','unverifiable')` - the objective accuracy axis. |
| `flags` | `text[]` NOT NULL default `{}` | Orthogonal manipulation flags: `missing-context`, `cherry-picked`, `outdated`, `misattributed`, `misleading-causation`. |
| `source_name` | `text` | Primary source name. |
| `source_url` | `text` | Primary source URL. |
| `quoted_span` | `text` default `''` | The exact span quoted from the source. |
| `outlet` | `text` | Fact-check outlet the record was sourced from. |
| `checked_at` | `timestamptz` NULL | When the outlet published its check. |
| `embedding` | `halfvec(1024)` NOT NULL | Claim vector. |
| `synced_at` | `timestamptz` | Last upsert time. |

- Index `political_claims_embedding_hnsw` (HNSW `halfvec_cosine_ops`, `m=16`, `ef_construction=200`).
- Retrieval: `SearchPoliticalClaims` orders by `embedding <=> query_embedding` (cosine distance).
- The crawler upserts content and embedding together text-form (`::halfvec`, never binary COPY).

### `voting_records` - structured AN/Senat scrutins (no embeddings)

Per-person recorded positions on dated scrutins, queried RELATIONALLY by (person, bill, date) - never
by cosine. The voting source adapter answers "did X vote for/against bill Y" from this store.

| Column | Type | Meaning |
|--------|------|---------|
| `person_id` | `text` | Deputy/senator id. Part of PK. |
| `person_name` | `text` | Display name. |
| `chamber` | `text` | CHECK in `('assemblee','senat')`. |
| `scrutin_id` | `text` | Scrutin id. Part of PK. |
| `bill_title` | `text` | Bill the scrutin was on. |
| `voted_on` | `date` | Scrutin date. |
| `position` | `text` | CHECK in `('for','against','abstain','absent')`. |
| `source_url` | `text` | AN/Senat open-data source URL. |
| `synced_at` | `timestamptz` | Last upsert time. |

- PK `(person_id, scrutin_id)` - one recorded position per person per scrutin, making re-ingest idempotent.
- Index `voting_records_person_bill_date_idx (person_id, bill_title, voted_on)` matches the lookup predicate.

## Tables that no longer exist

Do not look for these; they were dropped and have no reader or writer.

- `documents` - skeleton from `0001`, superseded by `claims` in `0002`.
- `segment_results`, `processed_videos` - batch fact-check tables, dropped in `0008`. Imported
  videos now stream live exactly like a live stream: the live path emits verdicts over the
  WebSocket and persists nothing. There is no stored per-segment verdict to query.

## Derived, not stored: confidence by closeness

A checked statement carries a **confidence score** aggregated over its retrieved match
cluster. It is computed at query time and streamed on the live result frame; nothing about it
is persisted (live results are ephemeral, see below). The score lives in exactly one place:
`stack/backend/internal/service/confidence.go` (`computeConfidence`), parameterised by the
matcher config (`ConfidenceClusterSize`, `ConfidenceLeadWeight`, `ConfidenceBodyWeight`).

**Inputs (all closeness-derived):**

- **Cosine similarity** of each match (`1 - distance`), the closeness of the spoken statement to
  the matched `claims` row or `evidence_chunks` row.
- **Curated-claim verdict** (`corroborates` / `contradicts` / `unclear`) - the signed stance of a
  `claims` match.
- **Chunk-kind weight** - a `evidence_chunks` evidence hit is scaled by its `kind`: `lead` at
  `ConfidenceLeadWeight`, `body` at `ConfidenceBodyWeight` (both in `[0, 1]`; an unknown kind
  defaults to the lead weight). A curated claim always weighs at its full similarity.

**Formula:** only the strongest `ConfidenceClusterSize` matches feed the score. Each match's
**weight** is its similarity (evidence scaled by chunk-kind weight); a corroborating claim and
every evidence hit add to **Supporting**, a contradicting claim adds to **Contradicting**, an
unclear claim (or a non-positive similarity) carries no stance and is ignored. Then:

```
score = Supporting / (Supporting + Contradicting)   bounded to [0, 1]
```

`score` is `0` when nothing stance-bearing corroborates the statement (the honest "no
corroboration" reading), distinct from an unchecked statement, which carries no score at all.

**Wire shape** (live result frame, snake_case; `internal/handler` `segmentJSON`):

- `confidence` (omitted on a skipped segment): `{ score, supporting, contradicting,
  evidence_items }` - the score plus the raw weights and contributing-match count it is derived
  from, so the number is explainable rather than opaque.
- each match's `contribution` - the stance-bearing weight that match added to the aggregate
  (`0` for an unclear claim, a non-positive similarity, or a match beyond the cluster cap). `kind`
  and `verdict` say which side it fell on; `contribution` is the magnitude. The per-match
  contributions of a checked segment sum to `supporting + contradicting`.

Coverage / checkability is still decided by `claims` alone (below); confidence only scores a
statement that was already deemed checkable. Documented for ingestion/scoring context in
`docs/ingestion-pipeline.md`.

## Cross-cutting facts (known landmines)

- Bulk vector loads MUST use text-format CSV `COPY`, never binary `CopyFrom`/binary `COPY`: pgx
  binary copy corrupts `halfvec` columns (phantom rows). `pg_dump`/`pg_restore` are safe (text I/O).
- Coverage / checkability is decided by `claims`; `evidence_chunks` is evidence-only. Populating the
  wiki corpus cannot change whether a segment is covered.
- Live fact-check results are ephemeral - streamed over the WebSocket, not stored. If asked "where
  are the verdicts for video X", the answer is "nowhere; they are emitted live, not persisted".
- All vectors are `halfvec(1024)` / Voyage `voyage-4-large`. A mismatch in dims or model is a bug,
  not a config choice.
