-- Inverse of 0014: drop the binary-quantization coarse index. The halfvec HNSW
-- (0013) is untouched, so the single-stage search keeps working.
DROP INDEX IF EXISTS evidence_chunks_embedding_bit_hnsw;
