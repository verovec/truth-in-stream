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

-- Regenerate the column under the fixed expression. Dropping the column drops
-- its index; the ADD rewrites the table and recomputes every existing row's
-- hash (convert_to is total, so the rewrite cannot fail on any content). The
-- index is recreated under its original name because the staging-swap code
-- recreates it by that name.
ALTER TABLE evidence_chunks DROP COLUMN content_hash;
ALTER TABLE evidence_chunks
    ADD COLUMN content_hash bytea
    GENERATED ALWAYS AS (immutable_sha256(content)) STORED;

CREATE INDEX evidence_chunks_source_content_hash
    ON evidence_chunks (source, content_hash);
