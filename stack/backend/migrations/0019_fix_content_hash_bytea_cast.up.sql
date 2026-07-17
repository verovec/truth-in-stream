-- 0018 computed the content fingerprint as sha256(content::bytea). A
-- text-to-bytea CAST does not copy the string's bytes: it PARSES the text as a
-- bytea input literal in escape format. Any content with a backslash sequence
-- that is not '\\' or an octal escape aborts the whole insert with 22P02 (a
-- frwiki page carrying LaTeX markup killed the bulk load), and content whose
-- sequences ARE valid escapes ('\\', '\101') hashes different bytes than the
-- text itself, silently diverging from the Go-side sha256-of-UTF-8 fingerprint
-- the embed short-circuit probes with.
--
-- convert_to(content, 'UTF8') is the byte-faithful conversion, but it is STABLE
-- (conversions are catalog-level objects) and a generated expression must be
-- IMMUTABLE. As with immutable_unaccent (0017), the wrapper pins search_path
-- and declares the fixed UTF8 conversion immutable, which holds because the
-- database encoding is UTF8 and a conversion's byte mapping never changes at
-- runtime.
CREATE OR REPLACE FUNCTION immutable_sha256(text)
    RETURNS bytea
    LANGUAGE sql
    IMMUTABLE
    PARALLEL SAFE
    STRICT
    SET search_path = pg_catalog
    AS $$ SELECT pg_catalog.sha256(pg_catalog.convert_to($1, 'UTF8')) $$;

-- The bulk-rebuild artifacts copy the generated column wholesale (staging is
-- created with LIKE evidence_chunks INCLUDING GENERATED and later renamed over
-- the live table at swap), so a staging table created under 0018 would
-- reinstate the broken expression the next time a bulk run resumes and swaps
-- it in. Both tables are transient - staging is a rebuild/resume target the
-- next bulk run recreates from the fixed live table, _old is leftover from an
-- interrupted swap - so drop them rather than repair them.
DROP TABLE IF EXISTS evidence_chunks_staging;
DROP TABLE IF EXISTS evidence_chunks_old;

-- Regenerate the column under the fixed expression. IF EXISTS keeps the deploy
-- unwedged on a database where the broken column was already dropped by hand
-- (0018's own down migration is the natural emergency unblock for the 22P02
-- crash). The ADD rewrites the table and rebuilds every index on it (the HNSW
-- and GIN indexes included) under ACCESS EXCLUSIVE; the recompute itself is
-- total (convert_to cannot fail on any content), but on a corpus of production
-- scale apply this in a maintenance window - or reset the corpus first, since
-- bulk ingest rebuilds it anyway. The index is recreated under its original
-- name because the staging-swap code recreates it by that name.
ALTER TABLE evidence_chunks DROP COLUMN IF EXISTS content_hash;
ALTER TABLE evidence_chunks
    ADD COLUMN content_hash bytea
    GENERATED ALWAYS AS (immutable_sha256(content)) STORED;

CREATE INDEX evidence_chunks_source_content_hash
    ON evidence_chunks (source, content_hash);
