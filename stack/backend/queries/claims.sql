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

-- name: LexicalSearchClaims :many
-- Lexical half of hybrid retrieval (VER-195): the top result_limit claims whose
-- French-folded search_vector matches the query terms, ranked by cover density
-- (ts_rank_cd weighs term proximity, which favours exact-figure and named-entity
-- overlap over raw term frequency). The GIN index on search_vector drives the @@
-- filter, so this is a bounded index scan, never a seq scan. The same
-- immutable_unaccent wrapper the generated column uses folds the query terms, so
-- accent matching is symmetric. The row also carries the cosine distance to
-- query_embedding so a fused lexical hit exposes the same wire-visible similarity
-- a vector hit does; the ORDER BY is the lexical rank, so the vector distance is
-- a carried attribute here and the HNSW index is not consulted. Ties break on id
-- for a stable ranking.
SELECT id, content, verdict, sources,
       (embedding <=> sqlc.arg(query_embedding))::float8 AS distance
FROM claims, websearch_to_tsquery('french', immutable_unaccent(sqlc.arg(query_text)::text)) AS q
WHERE search_vector @@ q
ORDER BY ts_rank_cd(search_vector, q) DESC, id
LIMIT sqlc.arg(result_limit);
