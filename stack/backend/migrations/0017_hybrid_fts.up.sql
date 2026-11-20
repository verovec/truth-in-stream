-- Hybrid retrieval (VER-195): a French lexical full-text branch alongside the
-- existing halfvec cosine vector branch, fused by Reciprocal Rank Fusion in the
-- store layer. Dense embeddings blur exactly where political fact-checking is
-- hardest - exact numbers, percentages, dates, named entities - so each corpus
-- the matcher and verify path retrieve from (claims and evidence_chunks) gains a
-- French-configured, accent-folded generated tsvector column with its own GIN
-- index. The vector HNSW indexes are untouched; the two branches run as two
-- independent indexed queries and fuse in Go, so no query mixes the two index
-- scans. political_claims is deferred to a later card if the eval justifies it.

CREATE EXTENSION IF NOT EXISTS unaccent;

-- unaccent() is only STABLE (it reads the 'unaccent' text-search dictionary),
-- but a GENERATED column expression must be IMMUTABLE. Pinning the dictionary by
-- name in a wrapper the schema owns makes IMMUTABLE honest: the dictionary is
-- fixed at deploy and changing its rules is a migration, not a runtime event (if
-- the rules file is ever edited, the stored columns must be regenerated). The
-- search_path is pinned to pg_catalog so the function cannot be shadowed into a
-- hijack, and it is the single accent-folding both the indexed content and the
-- query terms pass through, so lexical matching stays symmetric.
CREATE OR REPLACE FUNCTION immutable_unaccent(text)
    RETURNS text
    LANGUAGE sql
    IMMUTABLE
    PARALLEL SAFE
    STRICT
    SET search_path = pg_catalog
    AS $$ SELECT public.unaccent('public.unaccent'::regdictionary, $1) $$;

-- claims: content is the only free text a curated claim carries.
-- Adding a STORED generated column rewrites the table under an ACCESS EXCLUSIVE
-- lock; claims is small (curated), so this is cheap. The GIN index is a plain
-- CREATE INDEX because the rewrite already holds the table; on a large populated
-- table a later index would use CREATE INDEX CONCURRENTLY instead.
ALTER TABLE claims
    ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('french', immutable_unaccent(content))) STORED;

CREATE INDEX claims_search_vector_gin ON claims USING gin (search_vector);

-- evidence_chunks: both title and content carry retrievable French text (a named
-- entity in the title, a figure in the body), so the lexicon spans both. coalesce
-- guards the NOT NULL columns so the expression stays total even if a column is
-- later relaxed to nullable. This table can be large in production; the STORED
-- generated column addition rewrites it under an ACCESS EXCLUSIVE lock, so it is
-- an offline/maintenance migration on a populated corpus (the datastore is
-- re-ingestable, so a rebuild is the standard path).
ALTER TABLE evidence_chunks
    ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (
        to_tsvector('french', immutable_unaccent(coalesce(title, '') || ' ' || coalesce(content, '')))
    ) STORED;

CREATE INDEX evidence_chunks_search_vector_gin ON evidence_chunks USING gin (search_vector);
