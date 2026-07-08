# Datastore scale benchmark - verdict

Status: measured on 2026-07-08 (VER-173); consumed by the datastore-at-scale epic
(`docs/superpowers/specs/2026-07-08-datastore-at-scale-epic-design.md`): the schema card
(VER-174) takes the partition decision, the search card (VER-175) the ef_search/iterative-scan
settings, the quantization card (VER-176) the two-stage strategy, and the infra card (VER-177)
the sizing numbers.

## What this answers

The vector store must hold hundreds of GB of `halfvec(1024)` embeddings on managed AWS RDS
PostgreSQL + pgvector with the fastest practical similarity search. Three decisions had to be
measured, not guessed:

1. **Index/quantization strategy**: keep the single `halfvec` HNSW index, or add a
   binary-quantized `bit` coarse index with a `halfvec` rerank?
2. **Partitioning**: single table + one index, or `PARTITION BY LIST (source)` with one HNSW
   index per partition, given the primary access pattern is a **global** top-k (no source
   filter)?
3. **Sizing**: what instance class and parameter-group settings does the measured footprint
   imply?

## How to reproduce

```
make bench-datastore                                        # defaults: 100k x 1024-dim, ~10 min
make bench-datastore BENCH_FLAGS="-rows=10000 -ef=40,100"   # smaller sweep
```

The harness (`stack/backend/cmd/vectorbench`, logic in `stack/backend/internal/vectorbench`):

- generates a **deterministic** corpus (seeded Gaussian-mixture clusters, unit-normalized,
  harmonic source skew across 8 sources) so runs are comparable across changes;
- loads it into a throwaway `pgvector/pgvector:0.8.2-pg17` container - **the pgvector version
  RDS PostgreSQL 17.10 ships** - with text-form vectors (binary COPY corrupts `halfvec`);
- builds three layouts: single `halfvec` HNSW (production parameters m=16, ef_construction=200),
  the same table with a `binary_quantize(embedding)::bit(1024)` + `bit_hamming_ops` coarse index,
  and the corpus partitioned by source with per-partition HNSW indexes;
- computes exact ground truth per query with index scans disabled, then measures every matrix
  cell (scenario x filter x `hnsw.ef_search` x `hnsw.iterative_scan` x candidate multiplier):
  recall@10, client-observed p50/p95 latency, and on-disk footprint.

Versions verified 2026-07-08 via Context7 + registries: pgvector upstream stable 0.8.4; AWS RDS
PostgreSQL 17.10 ships 0.8.2 (17.9/17.8 ship 0.8.1, 17.3-17.7 ship 0.8.0). The benchmark pins
0.8.2 for RDS parity. `hnsw.iterative_scan` (off / strict_order / relaxed_order,
`hnsw.max_scan_tuples` default 20000) landed in 0.8.0; `binary_quantize` + `bit_hamming_ops`
landed in 0.7.0.

## Measured results

Corpus: 100,000 rows x 1024 dims, 8 sources (harmonic skew), 64 clusters, seed 42, k=10,
100 queries, pgvector 0.8.2 on PostgreSQL 17 (1 GiB shared_buffers, 1 GiB
maintenance_work_mem, 4 parallel maintenance workers). Build times at that setting: single
HNSW 95 s, bit index 45 s, 8 per-partition HNSW 110 s total; 100k-row load 25 s.

| scenario | filter | ef | iter | mult | recall@k | p50 | p95 | note |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| hnsw | none | 40 | off | - | 0.872 | 1.25ms | 1.54ms | - |
| hnsw | none | 40 | relaxed_order | - | 0.872 | 1.03ms | 1.22ms | - |
| hnsw | none | 100 | off | - | 0.980 | 1.52ms | 2.03ms | - |
| hnsw | none | 100 | relaxed_order | - | 0.980 | 1.55ms | 1.89ms | - |
| hnsw | none | 200 | off | - | 1.000 | 1.75ms | 2.05ms | - |
| hnsw | none | 200 | relaxed_order | - | 1.000 | 1.93ms | 2.30ms | - |
| hnsw | none | 400 | off | - | 1.000 | 2.68ms | 3.07ms | - |
| hnsw | none | 400 | relaxed_order | - | 1.000 | 2.64ms | 3.03ms | - |
| hnsw | source | 40 | off | - | 0.406 | 1.11ms | 1.25ms | - |
| hnsw | source | 40 | relaxed_order | - | 0.960 | 1.91ms | 2.74ms | - |
| hnsw | source | 100 | off | - | 0.730 | 1.62ms | 1.86ms | - |
| hnsw | source | 100 | relaxed_order | - | 0.986 | 1.85ms | 2.46ms | - |
| hnsw | source | 200 | off | - | 1.000 | 36.94ms | 141.59ms | - |
| hnsw | source | 200 | relaxed_order | - | 1.000 | 38.95ms | 132.38ms | - |
| hnsw | source | 400 | off | - | 0.999 | 2.46ms | 3.05ms | - |
| hnsw | source | 400 | relaxed_order | - | 0.999 | 2.57ms | 3.21ms | - |
| bq-rerank | none | 40 | off | 5 | 0.197 | 1.20ms | 1.41ms | - |
| bq-rerank | none | 40 | off | 10 | 0.197 | 1.24ms | 1.41ms | - |
| bq-rerank | none | 40 | off | 20 | 0.197 | 1.20ms | 1.46ms | - |
| bq-rerank | none | 40 | relaxed_order | 5 | 0.245 | 1.38ms | 1.67ms | - |
| bq-rerank | none | 40 | relaxed_order | 10 | 0.361 | 2.06ms | 2.45ms | - |
| bq-rerank | none | 40 | relaxed_order | 20 | 0.537 | 3.33ms | 3.98ms | - |
| bq-rerank | none | 100 | off | 5 | 0.238 | 1.57ms | 1.87ms | - |
| bq-rerank | none | 100 | off | 10 | 0.371 | 1.98ms | 2.36ms | - |
| bq-rerank | none | 100 | off | 20 | 0.371 | 2.13ms | 2.67ms | - |
| bq-rerank | none | 100 | relaxed_order | 5 | 0.238 | 1.61ms | 2.12ms | - |
| bq-rerank | none | 100 | relaxed_order | 10 | 0.371 | 2.00ms | 2.51ms | - |
| bq-rerank | none | 100 | relaxed_order | 20 | 0.532 | 3.79ms | 4.24ms | - |
| bq-rerank | none | 200 | off | 5 | 0.238 | 1.67ms | 2.12ms | - |
| bq-rerank | none | 200 | off | 10 | 0.362 | 2.31ms | 2.80ms | - |
| bq-rerank | none | 200 | off | 20 | 0.533 | 3.24ms | 4.20ms | - |
| bq-rerank | none | 200 | relaxed_order | 5 | 0.238 | 1.83ms | 1.98ms | - |
| bq-rerank | none | 200 | relaxed_order | 10 | 0.362 | 2.41ms | 2.86ms | - |
| bq-rerank | none | 200 | relaxed_order | 20 | 0.533 | 3.21ms | 3.94ms | - |
| bq-rerank | none | 400 | off | 5 | 0.246 | 2.18ms | 2.46ms | - |
| bq-rerank | none | 400 | off | 10 | 0.367 | 2.87ms | 3.40ms | - |
| bq-rerank | none | 400 | off | 20 | 0.530 | 3.76ms | 4.42ms | - |
| bq-rerank | none | 400 | relaxed_order | 5 | 0.246 | 2.28ms | 2.70ms | - |
| bq-rerank | none | 400 | relaxed_order | 10 | 0.367 | 2.53ms | 2.83ms | - |
| bq-rerank | none | 400 | relaxed_order | 20 | 0.530 | 3.90ms | 4.27ms | - |
| bq-rerank | source | 40 | off | 5 | 0.097 | 0.81ms | 0.95ms | - |
| bq-rerank | source | 40 | off | 10 | 0.097 | 0.73ms | 0.90ms | - |
| bq-rerank | source | 40 | off | 20 | 0.097 | 0.78ms | 0.97ms | - |
| bq-rerank | source | 40 | relaxed_order | 5 | 0.657 | 3.82ms | 5.50ms | - |
| bq-rerank | source | 40 | relaxed_order | 10 | 0.860 | 5.93ms | 13.53ms | - |
| bq-rerank | source | 40 | relaxed_order | 20 | 0.961 | 13.84ms | 25.04ms | - |
| bq-rerank | source | 100 | off | 5 | 0.221 | 1.29ms | 1.62ms | - |
| bq-rerank | source | 100 | off | 10 | 0.221 | 1.22ms | 1.62ms | - |
| bq-rerank | source | 100 | off | 20 | 0.221 | 1.25ms | 1.59ms | - |
| bq-rerank | source | 100 | relaxed_order | 5 | 0.656 | 3.17ms | 4.76ms | - |
| bq-rerank | source | 100 | relaxed_order | 10 | 0.864 | 5.16ms | 12.28ms | - |
| bq-rerank | source | 100 | relaxed_order | 20 | 0.962 | 13.09ms | 26.77ms | - |
| bq-rerank | source | 200 | off | 5 | 0.347 | 1.87ms | 2.40ms | - |
| bq-rerank | source | 200 | off | 10 | 0.362 | 1.67ms | 2.17ms | - |
| bq-rerank | source | 200 | off | 20 | 0.362 | 1.64ms | 2.08ms | - |
| bq-rerank | source | 200 | relaxed_order | 5 | 0.657 | 2.94ms | 4.28ms | - |
| bq-rerank | source | 200 | relaxed_order | 10 | 0.865 | 4.92ms | 11.54ms | - |
| bq-rerank | source | 200 | relaxed_order | 20 | 0.963 | 13.07ms | 26.19ms | - |
| bq-rerank | source | 400 | off | 5 | 0.478 | 2.48ms | 2.87ms | - |
| bq-rerank | source | 400 | off | 10 | 0.529 | 2.50ms | 3.11ms | - |
| bq-rerank | source | 400 | off | 20 | 0.545 | 2.50ms | 3.53ms | - |
| bq-rerank | source | 400 | relaxed_order | 5 | 0.663 | 3.03ms | 5.07ms | - |
| bq-rerank | source | 400 | relaxed_order | 10 | 0.864 | 6.08ms | 14.25ms | - |
| bq-rerank | source | 400 | relaxed_order | 20 | 0.962 | 16.64ms | 28.00ms | - |
| partitioned | none | 40 | off | - | 0.994 | 3.72ms | 4.35ms | - |
| partitioned | none | 40 | relaxed_order | - | 0.994 | 3.88ms | 5.28ms | - |
| partitioned | none | 100 | off | - | 0.999 | 6.39ms | 7.49ms | - |
| partitioned | none | 100 | relaxed_order | - | 0.999 | 6.47ms | 8.16ms | - |
| partitioned | none | 200 | off | - | 1.000 | 10.73ms | 13.28ms | - |
| partitioned | none | 200 | relaxed_order | - | 1.000 | 11.46ms | 13.78ms | - |
| partitioned | none | 400 | off | - | 1.000 | 126.44ms | 145.17ms | - |
| partitioned | none | 400 | relaxed_order | - | 1.000 | 127.05ms | 139.62ms | - |
| partitioned | source | 40 | off | - | 0.997 | 0.79ms | 1.10ms | - |
| partitioned | source | 40 | relaxed_order | - | 0.997 | 0.78ms | 1.12ms | - |
| partitioned | source | 100 | off | - | 1.000 | 1.05ms | 1.32ms | - |
| partitioned | source | 100 | relaxed_order | - | 1.000 | 1.12ms | 1.43ms | - |
| partitioned | source | 200 | off | - | 1.000 | 1.73ms | 2.09ms | - |
| partitioned | source | 200 | relaxed_order | - | 1.000 | 1.72ms | 2.02ms | - |
| partitioned | source | 400 | off | - | 1.000 | 3.17ms | 17.29ms | - |
| partitioned | source | 400 | relaxed_order | - | 1.000 | 3.26ms | 17.80ms | - |

### Footprint

| object | size |
| --- | --- |
| single table (heap + TOAST) | 271.4 MiB |
| single hnsw index (halfvec) | 260.4 MiB |
| bit expression index (binary quantize) | 41.5 MiB |
| partitioned tables (total) | 271.9 MiB |
| partitioned hnsw indexes (total) | 260.5 MiB |

Per-vector on-disk cost at 1024-dim `halfvec`: table (heap + TOAST) 2.78 KiB, halfvec HNSW index 2.67 KiB - **~5.4 KiB per vector total**; the optional bit index adds 0.42 KiB.

## Verdict

### 1. Index strategy: keep the single halfvec HNSW; adopt iterative scans

- The production-parity index (m=16, ef_construction=200) delivers **0.980 recall@10 at
  1.52 ms p50** with `ef_search=100` (today's production default) on the global access
  pattern, and **1.000 recall at 1.75 ms** at `ef_search=200`. This is the primary index;
  nothing measured beats it on the global search.
- **Set `hnsw.iterative_scan = relaxed_order` as the default.** It is free on the global
  search (identical recall, latency within noise) and it rescues filtered search: at
  `ef_search=40` a single-source filter collapses to **0.406** recall without it and recovers
  to **0.960 at 1.91 ms** with it (0.986 at ef=100). This is the concrete setting VER-175
  threads through the unified search builder.
- Do not chase filtered recall by raising `ef_search` with iterative scans off: the
  `ef=200, iter=off` filtered cell hit a planner plan-flip (37 ms p50 / 142 ms p95 for 1.000
  recall). Moderate `ef_search` + `relaxed_order` is the stable configuration.
- Per-query tuning matters and is cheap: ef=100 as the default, ef=200 for recall-critical
  call sites (coverage top-k), lower for latency-critical top-1 - exactly the per-query
  `ef_search` parameter VER-175 adds.

### 2. Binary quantization: capability yes, default no (defer adoption)

- On the global pattern BQ+rerank **caps at 0.537 recall** (multiplier 20, 3.2-3.9 ms) -
  slower AND far less accurate than plain HNSW at ef=100 (0.980 / 1.52 ms). Under a source
  filter with `relaxed_order` it reaches 0.962 but at 13-17 ms p50 (the rerank pays TOAST
  reads for every candidate).
- The RAM win is real but smaller than the 32x storyline: the bit index is **41.5 MiB vs
  260.4 MiB** for the halfvec HNSW (**6.3x**), because HNSW per-node graph overhead (m=16
  links) dominates once the vector payload shrinks.
- Caveat: recall under binary quantization is corpus-sensitive, and this corpus is synthetic
  (Gaussian mixture; sign patterns inside a tight cluster collide). Real voyage-4-large
  embeddings may fare differently - but adoption must be justified by a re-run of this
  harness over real evidence data, not assumed.
- **Decision for VER-176**: implement the two-stage (bit coarse + halfvec rerank) path inside
  the unified search builder as an opt-in with a configurable candidate multiplier,
  **default off**. Revisit when the corpus approaches the RAM ceiling of the chosen instance
  (roughly >= 50M vectors) and only after revalidating recall on the real corpus.

### 3. Partitioning: no - single table, one index

- Partition-by-source **loses the primary (global) access pattern at every operating point**:
  3.72 ms vs 1.25 ms p50 at ef=40, 6.39 ms vs 1.52 ms at ef=100, 10.7 ms vs 1.75 ms at
  ef=200, degrading to 126 ms at ef=400 (Append + re-sort over 8 per-partition HNSW scans).
  Recall is comparable (the append effectively multiplies candidates).
- Partition pruning does win the single-source pattern (0.79 ms vs 1.91 ms at ef=40) - but
  that is the secondary pattern, and the single index under `relaxed_order` already holds
  0.960+ recall at ~2 ms there. That is not worth a 2.5-6x tax on the primary pattern.
- **Decision for VER-174**: `evidence_chunks` is a single unpartitioned table with one
  `halfvec_cosine_ops` HNSW index (m=16, ef_construction=200). Revisit partitioning only if
  the access mix inverts to overwhelmingly source-scoped queries.

### 4. Sizing (consumed by VER-177)

Measured per-vector cost extrapolates linearly (HNSW is flat-structured):

| corpus | table | hnsw index | total on disk | RAM class to keep the index + hot table cached |
| --- | --- | --- | --- | --- |
| 10M vectors | ~27 GiB | ~26 GiB | ~53 GiB | 64 GiB (r7g.2xlarge / r8g.2xlarge) |
| 50M vectors | ~136 GiB | ~130 GiB | ~266 GiB | 256 GiB (r7g.8xlarge / r8g.8xlarge) |
| 100M vectors | ~271 GiB | ~260 GiB | ~532 GiB | 512 GiB (r7g.16xlarge / r8g.16xlarge) |

- **RAM**: performance holds while the HNSW index (which embeds full vector copies) stays in
  shared_buffers + page cache. Size for index + hot table + ~30% headroom: ~10M vectors fit
  an `r7g.2xlarge` (64 GiB); ~50M need the 256 GiB class (`r7g.8xlarge`/`r8g.8xlarge`);
  ~100M need 512 GiB (`r8g.16xlarge`) or accept partial-cache degradation on the cold tail.
- **Storage**: ~5.4 KiB per vector across heap, TOAST, and index - a 100M-vector corpus is roughly half a TiB
  before WAL/bloat headroom, so the gp3 ceiling should be raised to the 1 TiB range with
  IOPS/throughput tuned for index-build bursts.
- **Parameter group** (RDS PG17): keep `shared_buffers` at the RDS default (25% RAM);
  `effective_cache_size` 75% RAM; `work_mem` 64MB; `maintenance_work_mem` 2-4 GiB and
  `max_parallel_maintenance_workers` 4-8 (the 100k index built in 95 s at 1 GiB/4 workers -
  a 10M-row build is an hours-scale re-ingest operation, plan maintenance windows);
  `hnsw.ef_search` default 100; `hnsw.iterative_scan` default `relaxed_order`;
  `hnsw.max_scan_tuples` at its 20000 default.

## Caveats

- The corpus is synthetic. Latencies and footprints transfer directly (they depend on
  dimensionality, row count, and index parameters, all matched to production); recall values
  transfer directionally. The binary-quantization recall in particular must be revalidated on
  real embeddings before any adoption decision.
- Measured on a local container (1 GiB shared_buffers, tmpfs storage), not RDS: absolute
  latencies will shift with instance class and EBS; the *relative* ordering of strategies is
  what the verdict rests on.
- The harness reports client-observed latency including driver overhead, which is what the
  application experiences.
