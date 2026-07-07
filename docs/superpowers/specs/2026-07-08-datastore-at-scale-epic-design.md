# Datastore-at-scale epic - design

Date: 2026-07-08
Status: approved design; decomposed into cards (see "Card breakdown")

## Goal

Prepare the vector datastore for production at scale: store and search **hundreds of GB** of
embeddings with the **fastest practical similarity search**, and make it **cheap to add new
ingestion sources** - the pipeline will only get richer, so adding a source tomorrow must touch
the data structure as little as possible. Optimize the embedding search itself (recall/latency),
the storage footprint, and the schema's extensibility.

## Constraints and decisions

Three forks were decided up front:

1. **Substrate: stay on managed AWS RDS PostgreSQL + pgvector.** `pgvectorscale`, `VectorChord`,
   and `pg_diskann` all require `shared_preload_libraries` and custom binaries and are **not
   available on RDS or Aurora**; on managed RDS, pgvector is the only vector-index extension.
   Research (below) shows pgvector-only comfortably covers hundreds of GB / tens-to-low-hundreds
   of millions of vectors. Leaving RDS for a disk-native ANN engine is an escalation for
   billion-scale, documented but not built now.
2. **Partitioning: benchmark-gated, single-index first.** The evidence search is **global** (best
   evidence across all sources, no source filter today). Partition-by-source turns a global search
   into an `Append` + re-sort over N per-partition indexes, multiplying `ef_search` cost - it
   fights the primary query. So we start from a single generalized table + one index and adopt
   partitioning only if a benchmark proves it wins for the real query mix.
3. **Curated claim tables stay separate.** `claims` and `political_claims` are small, curated, and
   semantically distinct (different verdict models, different consumers). They are not the scale
   driver. They get the search-path and quantization improvements, but are not unified - unifying
   them is speculative risk against the coverage/confidence/political logic for no scale gain.

Additional constraint: the datastore is **greenfield for this work** - no production vector data
must be preserved. We can change schema, index, and config freely and **re-ingest every corpus
from source**. No dual-write/backfill migration is required.

## Current state (as of 2026-07-08)

Authoritative: `stack/backend/migrations/*.up.sql`, `stack/backend/queries/*.sql`.

- **Three embedding-bearing tables**, all `halfvec(1024)` (Voyage `voyage-4-large`), all HNSW
  `halfvec_cosine_ops` with `m=16, ef_construction=200`, **no partitioning anywhere, no IVFFlat**:
  - `claims` - curated verified claims; decides coverage/checkability.
  - `wiki_chunks` - the evidence corpus. Already holds **many sources** under a `corpus` text
    discriminator (encyclopedic wiki + `eurostat`, `interieur`, `insee-*`, category crawl).
    `embedding` is nullable (ingest writes rows un-embedded; a worker fleet fills vectors in place).
    PK `(page_id, chunk_index)` is Wikipedia-shaped. Bulk writes go through a staging table +
    atomic swap; vectors are written text-form (`::halfvec`), never binary (`pgx` binary copy
    corrupts `halfvec`).
  - `political_claims` - curated political claims (literal-verdict + `flags[]`); fast-path matcher.
  - (`voting_records` is relational, not vector; `videos` has no embedding.)
- **Search**: three near-identical queries (`embedding <=> $1`, ORDER BY, LIMIT). One global
  `hnsw.ef_search = 100` set per connection (`postgres.go`), no per-query override, no
  `iterative_scan`. The evidence search has **no `corpus` filter** - every source is one ANN pool.
- **Adding a source today**: a new stat/crawl evidence source = a new `corpus` label + rows (no
  migration); a new verdict-model corpus = a new table + migration.
- **Dimension is hard-coded in ~20 places**: `domain.EmbeddingDim = 1024` (the Go source of truth,
  used by ~10 validators), the `halfvec(1024)` DDL in every migration + the staging rebuild,
  config, docs, and the deterministic embedding seed cache. A model/dimension change is a large
  blast radius.
- **Scale config is undersized**: prod RDS is `db.t4g.small` (2 GiB RAM), gp3 20-to-100 GiB
  ceiling, **no RDS parameter group** (all memory/recall knobs are set transaction/session-locally
  in Go). Docs target "tens-of-millions" / "enwiki ~90 GB / r7g.2xlarge-class".

## Research summary (2026)

Verified against pgvector CHANGELOG, AWS RDS extension docs, timescale/pgvectorscale, VectorChord,
and partitioning write-ups.

- **pgvector**: latest stable **0.8.4** (RDS PG 17.10 currently ships **0.8.2**). Scale levers:
  `halfvec` (already used; fp16, half the storage of `vector`); **binary quantization**
  (`binary_quantize(v)::bit` + Hamming `<~>` as a coarse filter, then **rerank against the
  `halfvec` column** - about 32x smaller RAM working set); **iterative index scans** (0.8.0):
  `hnsw.iterative_scan = strict_order | relaxed_order` (+ `hnsw.max_scan_tuples`, default 20000)
  fixes filtered-search under-return so a `WHERE` filter does not tank recall. HNSW does not
  strictly need to fit in RAM but degrades sharply once it does not fit `shared_buffers`/OS cache;
  AWS recommends memory-optimized `r`-family. At 100-500 GB of vectors, size for the raw `halfvec`
  data + about 1.5-2x that for the HNSW index + cache headroom (multi-hundred-GB to ~1 TB RAM
  range).
- **pgvectorscale 0.9.0** (StreamingDiskANN + Statistical Binary Quantization + label-filtered
  search) is purpose-built for disk-resident billion-scale ANN, but **not available on RDS/Aurora**
  (needs `shared_preload_libraries`); requires self-managed Postgres (EC2) or Timescale Cloud.
  `VectorChord` and `pg_diskann` (Azure-only) hit the same RDS wall.
- **Partitioning**: `PARTITION BY LIST/HASH` on a source key with one HNSW index per partition is
  the standard scale pattern; partition pruning is a free prefilter **when the query filters by the
  partition key**. A query that cannot prune (a global top-k) becomes an `Append` over N indexes
  and costs more than one unified index. Optimizer support for merging ANN across partitions is not
  first-class yet.
- **Bottom line**: stay on RDS + pgvector; win with quantization + iterative scans + a real
  parameter group + `r7g/r8g` sizing; adopt partitioning only where the access pattern prunes.
  Escalate to pgvectorscale (EC2/Timescale) only at high-hundreds-of-millions-to-billions.

## Design - five pillars

### Pillar 1 - De-risk with a scale benchmark (do this first)
A repeatable, offline-ish harness that loads a representative large corpus (real or synthetic at a
representative row count and dimensionality) and measures recall@k, p50/p95 latency, and RAM/index
size across: (a) current `halfvec` HNSW single index; (b) `binary_quantize` + rerank; (c) single
index vs partition-by-source for the **global** evidence access pattern; over `ef_search` and
`hnsw.iterative_scan` settings. Output: a written verdict that fixes the index strategy, the
partition decision, and instance/parameter-group sizing for the later cards. This card gates the
schema/index cards so those are data-driven, not guessed.

### Pillar 2 - Generalize the evidence corpus (source-extensible schema)
Rename `wiki_chunks` to `evidence_chunks` (it already holds non-wiki corpora, so the name is a
misnomer). Keep the typed columns queries and ranking need (`source`, `content`, `embedding`,
`url`, `title`, `kind`, `synced_at`) and add a `metadata jsonb` for source-specific provenance
(revision id, section, stat-specific fields, cluster/importance), so **a new source is rows, not a
migration and not new columns**. Replace the Wikipedia-shaped PK with a generic key
(`(source, external_id, chunk_index)` or a surrogate id). Generalize `wiki_sync_state` accordingly.
Also **centralize the remaining `1024`/model hard-codes** so a future model swap is a config +
re-ingest, and write the model-migration runbook (trivial under greenfield/re-ingest: stand up the
new-dim schema, re-ingest, cut over). Respect the store boundary (sqlc; text-form `::halfvec` for
bulk writes; never binary copy). This is the largest card; it repoints the ingestion writers
(stats, crawl, wikisync) and the staging pipeline.

### Pillar 3 - Unified, tunable search path
Collapse the three near-identical search queries into one parameterized search builder. Add:
optional per-source filtering (`WHERE source = ANY($sources)`), a **per-query `ef_search`** (so
coverage top-k and the political top-1 can trade recall/latency independently), and
`hnsw.iterative_scan = relaxed_order` (+ a bounded `max_scan_tuples`) so a source/date filter does
not degrade recall. Preserves the global (unfiltered) search as the default. Touches the store
search functions and their service callers.

### Pillar 4 - Binary-quantization search
Add a two-stage search: a coarse `binary_quantize(embedding)::bit(1024)` index (Hamming distance)
to gather candidates, then **rerank the candidates against the `halfvec` column** for final cosine
ordering. This shrinks the RAM-resident working set about 32x - the key lever for hundreds of GB.
The exact strategy (candidate multiplier, whether the bit index replaces or complements the
halfvec HNSW) is set by Pillar 1's benchmark. Encapsulated in the Pillar 3 search builder.

### Pillar 5 - Infra sizing (Terraform; parallel; apply human-gated)
Add an RDS **parameter group** (`shared_buffers`, `work_mem`, `maintenance_work_mem`,
`max_parallel_maintenance_workers`, `effective_cache_size`, default `hnsw.ef_search` and
`hnsw.iterative_scan`). Right-size prod off `db.t4g.small` to a memory-optimized `r7g/r8g` class.
Raise the gp3 storage ceiling (100 GiB to hundreds of GB) and tune IOPS/throughput. Confirm the RDS
PG 17 minor carries pgvector >= 0.8.0 (iterative scans). Terraform-only; independent of the Go
cards, so it runs in parallel. The `terraform apply` stays human-gated.

## Card breakdown

Cards 2 -> 3 -> 4 share the store/queries files, so they are a **stacked dependency chain**
(`depends_on` in order), not parallel. Card 1 gates Card 2. Card 5 (Terraform) is file-disjoint and
runs in parallel.

1. **Scale benchmark harness** - measure index/quantization/partition options; write the verdict.
   (parallel-safe; gates card 2)
2. **Generalize the evidence corpus** - `evidence_chunks` + `metadata jsonb` + generic PK + dim/
   model centralization + ingestion/staging repoint + re-ingest. (depends on 1)
3. **Unified, tunable search path** - one search builder, per-source filter, per-query `ef_search`,
   iterative scans. (depends on 2)
4. **Binary-quantization search** - coarse `bit` + rerank on `halfvec`. (depends on 3; strategy
   from 1)
5. **RDS sizing + parameter group** - r-family instance, parameter group, storage/IOPS ceiling,
   pgvector version. (parallel; Terraform; apply human-gated)

## Escalation path (documented, not built)

If growth pushes past the high-hundreds-of-millions-to-billions range where even a large `r8g`
cannot economically hold the working set, move the evidence corpus to **pgvectorscale
(StreamingDiskANN + SBQ)** on self-managed Postgres (EC2) or Timescale Cloud, or **VectorChord**.
This is a substrate + ops change (own HA/patching/backups or onboard a vendor) and is out of scope
here; the pgvector schema is wire-compatible enough that the migration is index/host, not a rewrite.

## Risks / open questions

- The partition decision depends entirely on Pillar 1's measured query mix; do not pre-commit the
  schema to a partition key.
- `metadata jsonb` must not absorb fields that queries filter or rank on - those stay typed columns
  so the planner and sqlc keep working.
- Binary-quantization recall must be validated per corpus (recall is dataset-size sensitive); the
  rerank stage is what protects final ordering.
- Instance right-sizing is a cost decision the operator owns; the benchmark provides the numbers.
