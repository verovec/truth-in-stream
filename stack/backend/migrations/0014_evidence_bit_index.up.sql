-- Binary-quantization coarse index for the two-stage evidence search (VER-176).
-- binary_quantize(embedding)::bit(1024) is the sign-bit reduction of the halfvec:
-- a Hamming-distance HNSW over it is a ~6x smaller RAM working set than the
-- halfvec HNSW (the VER-173 benchmark measured 41 MiB vs 260 MiB at 100k rows),
-- so the coarse stage gathers candidates cheaply and a rerank against the
-- full-precision halfvec column restores ranking. It COMPLEMENTS the halfvec
-- HNSW (0013) rather than replacing it - the global unfiltered search still uses
-- the halfvec index; only the opt-in two-stage path uses this one - so both
-- exist. The pattern (binary_quantize + bit_hamming_ops + the <~> Hamming
-- operator) needs pgvector >= 0.7.0, well below the 0.8.2 the epic pins.
--
-- Same m/ef_construction as evidence_chunks_embedding_hnsw so build time and
-- recall behaviour are comparable; the expression must match the coarse query's
-- ORDER BY expression exactly (binary_quantize(embedding)::bit(1024)) for the
-- planner to use the index. The dimension 1024 is the SQL half of the one-place
-- dimension contract (the Go half is domain.EmbeddingDim); see
-- docs/embedding-model-migration.md.
CREATE INDEX evidence_chunks_embedding_bit_hnsw
    ON evidence_chunks USING hnsw ((binary_quantize(embedding)::bit(1024)) bit_hamming_ops)
    WITH (m = 16, ef_construction = 200);
