# Wikipedia Ingestion and Embedding - Design

Date: 2026-06-10
Status: Approved (design review with owner)
Scope: Wikipedia corpus ingestion, embedding, freshness sync, evidence matching, Terraform readiness.
Out of scope: live-stream transcription, WebSocket/SSE push to the frontend (separate later phase).

## Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Corpus | Corpus-agnostic pipeline; `simplewiki` locally, `enwiki` (lead sections) on AWS later | Cheap local dev (~250k articles, <1GB); real corpus only when deployed. Deployment is never an acceptance criterion; Terraform is kept ready. |
| Freshness | Periodic re-sync (weekly), revision-ID diff | Fact-checking rarely hinges on day-old edits. Near-zero idle cost. EventStreams SSE deferred to the live phase. |
| Ingestion mechanism | Single batch binary, no message broker | Voyage TPM is the bottleneck; queues/workers (SQS, RabbitMQ) add idle cost and ops for no throughput gain. Idempotency via revision IDs, not queue semantics. |
| Wikipedia content role | Evidence, not verdicts | Curated claims carry corroborates/contradicts; wiki chunks are supporting context with mandatory CC BY-SA attribution. |
| Frontend transport | Keep polling | No WebSocket/SSE in this phase. |

## Verified facts (2026-06)

- Voyage pricing: voyage-4 $0.06/M tokens, voyage-4-lite $0.02/M, voyage-4-large $0.12/M;
  200M free tokens per account; tier-1 limits 2000 RPM / 8M TPM; Files/Batch API at 33% off.
  Source: docs.voyageai.com/docs/pricing, /docs/rate-limits (web-verified 2026-06-10).
- simplewiki dump: ~250MB compressed multistream, ~250k articles. enwiki: ~24GB compressed,
  ~7M articles. Verify exact sizes at dumps.wikimedia.org/{wiki}/latest/ at implementation time.
- pgvector halfvec(1024) + HNSW m=16 footprint: ~0.8GB at 250k chunks, ~22GB at 7M.
  COPY-then-index is 5-10x faster than incremental inserts; raise `maintenance_work_mem`
  for builds. (Training-data estimates; re-verify pgvector release notes at implementation.)
- MediaWiki `list=recentchanges` retains 30 days; `prop=extracts&exintro&explaintext`
  batches 50 titles/request. Bot policy requires a descriptive User-Agent.
- Licensing: CC BY-SA 4.0. Displaying snippets to users requires attribution
  (article title + URL). Storing vectors internally is processing, not redistribution.

## Architecture

### Components

- `internal/wiki` - dump download, multistream parsing, lead-section extraction,
  chunking, recentchanges delta detection. No HTTP handler types, no store types
  beyond the domain interfaces.
- `cmd/wikisync` - wiring only. Modes: `bulk` (full corpus load) and `delta`
  (periodic re-sync). Flags: `-mode`, `-dry-run`. Corpus from `WIKI_CORPUS` env.
- Reuses `internal/embed` (Voyage client, `input_type=document`) and the
  postgres store package (new wiki store alongside the claims store).

### Schema (migration 0004)

`wiki_chunks`:
- `page_id bigint`, `chunk_index int` - PK `(page_id, chunk_index)`
- `title text`, `url text`, `revision_id bigint`, `corpus text`
- `content text`, `embedding halfvec(1024)`, `synced_at timestamptz`
- HNSW index `halfvec_cosine_ops`, m=16, ef_construction=200 (matches `claims`)

`wiki_sync_state`:
- `corpus text` PK, `last_change_ts timestamptz`, `dump_version text`, `synced_at timestamptz`

`claims` is untouched. Separate tables per corpus keep the shadow-swap rebuild trivial.

### Bulk flow

1. Stream-download `{corpus}-latest-pages-articles-multistream.xml.bz2` + index file.
2. Parallel cluster decompression (goroutines over index offsets).
3. Per article: skip redirects/disambiguations; extract lead section (wikitext before
   the first `==` heading), strip markup to plain text.
4. Chunk at ~256-512 tokens on paragraph boundaries, title prepended to every chunk:
   `"{title}\n\n{lead-chunk}"`.
5. `-dry-run`: print article/chunk/token counts and cost estimate, stop before embedding.
6. Embed in batches (existing 128/request default), concurrency-capped, exponential
   backoff on 429.
7. COPY into `wiki_chunks_staging` (no index), build HNSW after load with raised
   `maintenance_work_mem`, then transactional rename swap staging -> live.
8. Record `dump_version` and checkpoint in `wiki_sync_state`.

Re-runnable: pages already in staging with embeddings are skipped on restart.

### Delta flow (scheduled, weekly)

1. Query `list=recentchanges` (namespace 0) since `last_change_ts` checkpoint.
2. Keep titles whose new `revid` differs from the stored `revision_id`.
3. Batch-refetch lead sections via `prop=extracts&exintro&explaintext` (50/request).
4. Re-chunk, re-embed, upsert in place (delete stale chunk rows for shrunk articles;
   delete all chunks for deleted pages).
5. Advance the checkpoint only after all upserts commit.

Incremental HNSW inserts are acceptable at delta volume. If a delta window exceeds a
configured threshold (e.g. >10% of corpus), log and recommend a bulk re-run instead.

### Matching and API

- `MatchSegment` searches `claims` and `wiki_chunks`, merges by similarity.
- `SegmentMatch` gains `kind: "claim" | "evidence"`. Evidence entries carry
  `title`, `url`, `similarity`; no verdict.
- `GET /api/videos/{id}/results` exposes the new field. Stored JSONB shape extends
  compatibly (absent `kind` reads as `"claim"`).
- Fact-check panel renders evidence rows with Wikipedia attribution (title linked
  to the article URL, "Wikipedia, CC BY-SA" credit).

### Local vs AWS

- Local: `WIKI_CORPUS=simplewiki`; ingest via `docker compose run` / make target.
  Fits the existing postgres container and t4g.micro-class resources.
- AWS (later, not gated): `enwiki` lead sections. New Terraform `scheduled-task`
  module (EventBridge Scheduler -> one-shot Fargate task, same shape as the existing
  `migration` module) running `wikisync -mode=delta` on a cron variable. RDS sizing
  exposed as documented variables: prod enwiki target r7g.2xlarge-class, 500GB gp3,
  `maintenance_work_mem` raised for index builds. `fmt`/`validate` clean; applying
  is a human decision later.

### Testing

- Committed tiny multistream bz2 fixture for parser tests.
- Table-driven tests: lead extraction, wikitext stripping, chunker boundaries.
- Fake embedder for pipeline tests; `httptest` mocks for MediaWiki APIs.
- Store integration tests on the existing shared-DB pattern (staging swap, upserts,
  sync-state checkpointing).
- Vitest: evidence rendering and attribution in the panel.
- `go test -race ./...`, `go vet`, `golangci-lint`, ESLint - all green per house rules.

## Card breakdown

| # | Card | Depends on |
|---|---|---|
| 1 | Wiki corpus schema + dump parsing + lead extraction + chunk storage | - |
| 2 | Bulk embedding pipeline: batching, staging COPY, HNSW build, atomic swap, dry-run | 1 |
| 3 | Periodic delta sync: recentchanges scan, revid diff, re-embed, checkpoint | 2 |
| 4 | Evidence matching end-to-end: matcher + API `kind` + panel attribution | 2 |
| 5 | Terraform: scheduled-task module + RDS sizing variables/docs (no apply) | - |

## Open items deferred to the live phase

- EventStreams SSE consumer for minute-level freshness.
- Scribe v2 Realtime WebSocket transcription.
- Push transport (SSE/WebSocket) from backend to the fact-check panel.
