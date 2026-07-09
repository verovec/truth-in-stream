# Embedding-model migration runbook

Changing the embedding model or its dimension is a **config change plus a full re-ingest**, not a
schema patch. The datastore is greenfield by design (no production vector data is preserved), so
there is no in-place backfill: you stand up the new-dimension schema, re-ingest every corpus from
source, and cut over. This runbook is the deliberate procedure.

## Where the model and dimension live (the one-place contract)

The embedding model and dimension each have a single source of truth; everything else derives from
or is validated against them.

- **Model name**: `config.DefaultEmbeddingModel` (`stack/backend/internal/config/config.go`),
  resolved through `config.EmbeddingModel()` (env `EMBEDDING_MODEL` overrides). The seed cache keys
  on the same resolution, so offline seeding can never disagree with the running stack.
- **Dimension, Go half**: `domain.EmbeddingDim` (`stack/backend/internal/domain/claim.go`). Every
  validator (`internal/store/postgres`, `internal/embedjob`, `internal/crawljob`, `internal/service`,
  `config.LoadEmbedding`) compares against it, and `domain.HalfvecColumnType()` derives the column
  type string the corpus verifier checks, so there is no second literal to keep in sync.
- **Dimension, SQL half**: the `halfvec(1024)` column type in the migrations
  (`stack/backend/migrations/*.up.sql`) - `evidence_chunks`, `claims`, `political_claims`. SQL cannot
  reference a Go constant, so the migration DDL is the authoritative SQL declaration; the Go
  validators + `domain.HalfvecColumnType()` prove the live column matches `EmbeddingDim` at verify
  time.
- **Config guard**: `EMBEDDING_DIM`, if set, must equal `domain.EmbeddingDim` or the process fails
  fast (`config.LoadEmbedding`) - a mismatched env can never silently produce garbage vectors.

## Procedure

1. **Pick the model and dimension.** Confirm the Voyage model's output dimension. If you are only
   swapping the model at the same dimension (e.g. a same-1024-dim successor), skip step 2.
2. **Change the dimension in both halves** (only if the dimension changes):
   - Go: set `domain.EmbeddingDim` to the new value. `domain.HalfvecColumnType()` and every
     validator follow automatically.
   - SQL: change `halfvec(1024)` to `halfvec(<new-dim>)` in every embedding column across the
     migrations. Because the datastore is greenfield, prefer editing the existing DDL and rebuilding
     from scratch over adding an ALTER migration (there is no data to preserve).
3. **Change the model** (if swapping): set `EMBEDDING_MODEL` in the environment, or change
   `config.DefaultEmbeddingModel` to make the new model the default.
4. **Regenerate the deterministic seed cache** so offline seeding produces new-dimension vectors:
   `make refresh-embeddings` (needs `EMBEDDING_API_KEY`; it re-embeds the committed fixtures through
   the new model at the new dimension and rewrites `stack/backend/seed/embeddings.cache.jsonl`).
5. **Stand up the new-dimension schema.** On a fresh database, `make migrate` applies the updated
   DDL; the vector columns are now `halfvec(<new-dim>)`.
6. **Re-ingest every corpus from source.** Nothing carries over.
   - Evidence corpus (Wikipedia + crawl): `make reingest` (atomic rebuild) or `make wiki-populate`
     (bulk-into-live), then `make wiki-cluster`.
   - Statistical sources: `make stats-ingest`.
   - Curated claims and political claims: `make seed-claims` and the fact-check crawl.
   The worker fleet embeds each corpus through the new model; the atomic swap or bulk-into-live path
   makes it searchable as it fills.
7. **Verify.** `make wiki-verify` gates on the embedding column being exactly
   `domain.HalfvecColumnType()` (the new dimension), no zero vectors, populated metadata, and a live
   HNSW index. A green verify means the corpus is rebuilt at the new dimension and consistent.
8. **Production cutover** stays human-gated: apply the schema and roll the services deliberately
   (see the deploy rules in `CLAUDE.md`). Because prod is greenfield too, the cutover is a fresh
   stand-up and re-ingest, not an online migration.

## Why re-ingest rather than backfill

A model or dimension change makes every stored vector incomparable with new query vectors -
different geometry, sometimes a different width - so a partial or dual-write migration would serve
mixed, meaningless similarity scores during the transition. Re-ingesting from source is the only
correct path, and the pipeline already supports it cheaply (bulk-into-live keeps the corpus
queryable while the fleet refills it). The generalized `evidence_chunks` schema (a new source is
rows under a new `source` value, provenance in `metadata jsonb`) means adding or re-ingesting a
source needs no migration at all - only the dimension change does, and only in the two places above.
