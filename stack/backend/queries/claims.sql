-- name: UpsertClaim :batchexec
INSERT INTO claims (id, content, verdict, sources, embedding)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE
    SET content = EXCLUDED.content,
        verdict = EXCLUDED.verdict,
        sources = EXCLUDED.sources,
        embedding = EXCLUDED.embedding;

-- name: SearchClaims :many
-- Named arg query_embedding is referenced twice but sqlc collapses it to a
-- single parameter, so the HNSW index still drives the ORDER BY (no repeated
-- positional-parameter mis-numbering).
SELECT id, content, verdict, sources, (embedding <=> sqlc.arg(query_embedding))::float8 AS distance
FROM claims
ORDER BY embedding <=> sqlc.arg(query_embedding)
LIMIT sqlc.arg(result_limit);
