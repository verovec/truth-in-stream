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
HNSW 88 s, bit index 40 s, 8 per-partition HNSW 67 s total; 100k-row load 22 s.

| scenario | filter | ef | iter | mult | recall@k | p50 | p95 | note |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| hnsw | none | 40 | off | - | 0.870 | 1.10ms | 1.35ms | - |
| hnsw | none | 40 | relaxed_order | - | 0.870 | 1.13ms | 1.27ms | - |
| hnsw | none | 100 | off | - | 0.980 | 1.47ms | 1.77ms | - |
| hnsw | none | 100 | relaxed_order | - | 0.980 | 1.54ms | 1.78ms | - |
| hnsw | none | 200 | off | - | 1.000 | 1.86ms | 2.15ms | - |
| hnsw | none | 200 | relaxed_order | - | 1.000 | 1.79ms | 1.97ms | - |
| hnsw | none | 400 | off | - | 1.000 | 2.27ms | 3.51ms | - |
| hnsw | none | 400 | relaxed_order | - | 1.000 | 2.25ms | 2.86ms | - |
| hnsw | source | 40 | off | - | 0.406 | 0.94ms | 1.11ms | - |
| hnsw | source | 40 | relaxed_order | - | 0.959 | 1.96ms | 3.12ms | - |
| hnsw | source | 100 | off | - | 0.730 | 1.67ms | 1.84ms | - |
| hnsw | source | 100 | relaxed_order | - | 0.986 | 1.83ms | 2.37ms | - |
| hnsw | source | 200 | off | - | 1.000 | 35.66ms | 122.50ms | - |
| hnsw | source | 200 | relaxed_order | - | 1.000 | 38.71ms | 137.52ms | - |
| hnsw | source | 400 | off | - | 0.999 | 2.80ms | 3.50ms | - |
| hnsw | source | 400 | relaxed_order | - | 0.999 | 2.42ms | 3.02ms | - |
| bq-rerank | none | 40 | off | 5 | 0.201 | 1.22ms | 1.49ms | - |
| bq-rerank | none | 40 | off | 10 | 0.201 | 1.28ms | 1.88ms | - |
| bq-rerank | none | 40 | off | 20 | 0.201 | 1.07ms | 1.29ms | - |
| bq-rerank | none | 40 | relaxed_order | 5 | 0.239 | 1.46ms | 1.94ms | - |
| bq-rerank | none | 40 | relaxed_order | 10 | 0.367 | 2.09ms | 2.48ms | - |
| bq-rerank | none | 40 | relaxed_order | 20 | 0.532 | 3.21ms | 4.09ms | - |
| bq-rerank | none | 100 | off | 5 | 0.239 | 1.43ms | 1.67ms | - |
| bq-rerank | none | 100 | off | 10 | 0.370 | 1.93ms | 2.31ms | - |
| bq-rerank | none | 100 | off | 20 | 0.370 | 1.97ms | 2.19ms | - |
| bq-rerank | none | 100 | relaxed_order | 5 | 0.239 | 1.44ms | 1.74ms | - |
| bq-rerank | none | 100 | relaxed_order | 10 | 0.370 | 1.83ms | 2.29ms | - |
| bq-rerank | none | 100 | relaxed_order | 20 | 0.532 | 3.06ms | 4.24ms | - |
| bq-rerank | none | 200 | off | 5 | 0.240 | 1.68ms | 1.89ms | - |
| bq-rerank | none | 200 | off | 10 | 0.367 | 2.35ms | 3.12ms | - |
| bq-rerank | none | 200 | off | 20 | 0.530 | 3.16ms | 3.46ms | - |
| bq-rerank | none | 200 | relaxed_order | 5 | 0.240 | 1.83ms | 2.18ms | - |
| bq-rerank | none | 200 | relaxed_order | 10 | 0.367 | 2.14ms | 2.52ms | - |
| bq-rerank | none | 200 | relaxed_order | 20 | 0.530 | 3.43ms | 4.49ms | - |
| bq-rerank | none | 400 | off | 5 | 0.244 | 2.07ms | 3.74ms | - |
| bq-rerank | none | 400 | off | 10 | 0.369 | 2.60ms | 3.29ms | - |
| bq-rerank | none | 400 | off | 20 | 0.532 | 3.63ms | 4.68ms | - |
| bq-rerank | none | 400 | relaxed_order | 5 | 0.244 | 2.31ms | 2.81ms | - |
| bq-rerank | none | 400 | relaxed_order | 10 | 0.369 | 2.70ms | 3.44ms | - |
| bq-rerank | none | 400 | relaxed_order | 20 | 0.532 | 3.72ms | 6.51ms | - |
| bq-rerank | source | 40 | off | 5 | 0.097 | 0.76ms | 1.01ms | - |
| bq-rerank | source | 40 | off | 10 | 0.097 | 0.71ms | 0.88ms | - |
| bq-rerank | source | 40 | off | 20 | 0.097 | 0.72ms | 0.89ms | - |
| bq-rerank | source | 40 | relaxed_order | 5 | 0.653 | 3.26ms | 4.80ms | - |
| bq-rerank | source | 40 | relaxed_order | 10 | 0.861 | 5.06ms | 11.93ms | - |
| bq-rerank | source | 40 | relaxed_order | 20 | 0.963 | 13.22ms | 23.44ms | - |
| bq-rerank | source | 100 | off | 5 | 0.220 | 1.07ms | 1.34ms | - |
| bq-rerank | source | 100 | off | 10 | 0.220 | 1.10ms | 1.44ms | - |
| bq-rerank | source | 100 | off | 20 | 0.220 | 1.38ms | 1.78ms | - |
| bq-rerank | source | 100 | relaxed_order | 5 | 0.654 | 3.04ms | 4.41ms | - |
| bq-rerank | source | 100 | relaxed_order | 10 | 0.864 | 5.09ms | 11.70ms | - |
| bq-rerank | source | 100 | relaxed_order | 20 | 0.962 | 13.53ms | 25.51ms | - |
| bq-rerank | source | 200 | off | 5 | 0.349 | 1.51ms | 1.90ms | - |
| bq-rerank | source | 200 | off | 10 | 0.362 | 1.69ms | 2.65ms | - |
| bq-rerank | source | 200 | off | 20 | 0.362 | 1.84ms | 2.43ms | - |
| bq-rerank | source | 200 | relaxed_order | 5 | 0.660 | 3.25ms | 5.12ms | - |
| bq-rerank | source | 200 | relaxed_order | 10 | 0.861 | 5.47ms | 13.72ms | - |
| bq-rerank | source | 200 | relaxed_order | 20 | 0.963 | 13.91ms | 25.09ms | - |
| bq-rerank | source | 400 | off | 5 | 0.475 | 2.49ms | 3.56ms | - |
| bq-rerank | source | 400 | off | 10 | 0.526 | 2.25ms | 2.89ms | - |
| bq-rerank | source | 400 | off | 20 | 0.545 | 2.56ms | 3.58ms | - |
| bq-rerank | source | 400 | relaxed_order | 5 | 0.657 | 3.63ms | 5.24ms | - |
| bq-rerank | source | 400 | relaxed_order | 10 | 0.860 | 6.37ms | 15.90ms | - |
| bq-rerank | source | 400 | relaxed_order | 20 | 0.964 | 14.33ms | 24.82ms | - |
| partitioned | none | 40 | off | - | 0.994 | 3.62ms | 4.23ms | - |
| partitioned | none | 40 | relaxed_order | - | 0.994 | 4.01ms | 4.86ms | - |
| partitioned | none | 100 | off | - | 0.999 | 5.57ms | 7.59ms | - |
| partitioned | none | 100 | relaxed_order | - | 0.999 | 6.59ms | 8.35ms | - |
| partitioned | none | 200 | off | - | 1.000 | 10.25ms | 11.94ms | - |
| partitioned | none | 200 | relaxed_order | - | 1.000 | 11.44ms | 13.24ms | - |
| partitioned | none | 400 | off | - | 1.000 | 132.86ms | 151.68ms | - |
| partitioned | none | 400 | relaxed_order | - | 1.000 | 137.07ms | 150.60ms | - |
| partitioned | source | 40 | off | - | 0.997 | 0.74ms | 1.03ms | - |
| partitioned | source | 40 | relaxed_order | - | 0.997 | 0.85ms | 1.25ms | - |
| partitioned | source | 100 | off | - | 1.000 | 1.19ms | 1.54ms | - |
| partitioned | source | 100 | relaxed_order | - | 1.000 | 1.09ms | 1.33ms | - |
| partitioned | source | 200 | off | - | 1.000 | 1.74ms | 2.45ms | - |
| partitioned | source | 200 | relaxed_order | - | 1.000 | 1.84ms | 2.29ms | - |
| partitioned | source | 400 | off | - | 1.000 | 3.08ms | 16.23ms | - |
| partitioned | source | 400 | relaxed_order | - | 1.000 | 3.31ms | 16.81ms | - |

### Footprint

| object | size |
| --- | --- |
| single table (heap + TOAST) | 271.4 MiB |
| single hnsw index (halfvec) | 260.4 MiB |
| bit expression index (binary quantize) | 41.4 MiB |
| partitioned tables (total) | 271.9 MiB |
| partitioned hnsw indexes (total) | 260.5 MiB |

Per-vector on-disk cost at 1024-dim `halfvec`: table (heap + TOAST) 2.78 KiB, halfvec HNSW index 2.67 KiB - **~5.4 KiB per vector total**; the optional bit index adds 0.42 KiB.

## Verdict

### 1. Index strategy: keep the single halfvec HNSW; adopt iterative scans

- The production-parity index (m=16, ef_construction=200) delivers **0.980 recall@10 at
  1.47 ms p50** with `ef_search=100` (today's production default) on the global access
  pattern, and **1.000 recall at 1.86 ms** at `ef_search=200`. This is the primary index;
  nothing measured beats it on the global search.
- **Set `hnsw.iterative_scan = relaxed_order` as the default.** It is free on the global
  search (identical recall, latency within noise) and it rescues filtered search: at
  `ef_search=40` a single-source filter collapses to **0.406** recall without it and recovers
  to **0.959 at 1.96 ms** with it (0.986 at ef=100). This is the concrete setting VER-175
  threads through the unified search builder.
- Do not chase filtered recall by raising `ef_search` with iterative scans off: the
  `ef=200, iter=off` filtered cell hit a planner plan-flip (36 ms p50 / 123 ms p95 for 1.000
  recall). Moderate `ef_search` + `relaxed_order` is the stable configuration.
- Per-query tuning matters and is cheap: ef=100 as the default, ef=200 for recall-critical
  call sites (coverage top-k), lower for latency-critical top-1 - exactly the per-query
  `ef_search` parameter VER-175 adds.

### 2. Binary quantization: capability yes, default no (defer adoption)

- On the global pattern BQ+rerank **caps at 0.532 recall** (multiplier 20, 3.1-3.7 ms) -
  slower AND far less accurate than plain HNSW at ef=100 (0.980 / 1.47 ms). Under a source
  filter with `relaxed_order` it reaches 0.963 but at 13-14 ms p50 (the rerank pays TOAST
  reads for every candidate).
- The RAM win is real but smaller than the 32x storyline: the bit index is **41.4 MiB vs
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
  3.62 ms vs 1.10 ms p50 at ef=40, 5.57 ms vs 1.47 ms at ef=100, 10.3 ms vs 1.86 ms at
  ef=200, degrading to 133 ms at ef=400 (Append + re-sort over 8 per-partition HNSW scans).
  Recall is comparable (the append effectively multiplies candidates).
- Partition pruning does win the single-source pattern (0.74 ms vs 1.96 ms at ef=40) - but
  that is the secondary pattern, and the single index under `relaxed_order` already holds
  ~0.96 recall at ~2 ms there. That is not worth a 3.3-5.5x tax on the primary pattern.
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
  `max_parallel_maintenance_workers` 4-8 (the 100k index built in 88 s at 1 GiB/4 workers -
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
