-- name: UpsertDocument :batchexec
INSERT INTO documents (id, content, metadata, embedding)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE
    SET content = EXCLUDED.content,
        metadata = EXCLUDED.metadata,
        embedding = EXCLUDED.embedding;

-- name: SearchDocuments :many
-- Named arg query_embedding is referenced twice but sqlc collapses it to a
-- single parameter, so the HNSW index still drives the ORDER BY (no repeated
-- positional-parameter mis-numbering).
SELECT id, content, metadata, (embedding <=> sqlc.arg(query_embedding))::float8 AS distance
FROM documents
ORDER BY embedding <=> sqlc.arg(query_embedding)
LIMIT sqlc.arg(result_limit);
