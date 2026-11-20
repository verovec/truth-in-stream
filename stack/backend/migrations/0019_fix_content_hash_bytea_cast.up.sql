-- 0018 computed the content fingerprint as sha256(content::bytea). A
-- text-to-bytea CAST does not copy the string's bytes: it PARSES the text as a
-- bytea input literal in escape format. Any content with a backslash sequence
-- that is not '\\' or an octal escape aborts the whole insert with 22P02 (a
-- frwiki page carrying LaTeX markup killed the bulk load), and content whose
-- sequences ARE valid escapes ('\\', '\101') hashes different bytes than the
-- text itself, silently diverging from the Go-side sha256-of-UTF-8 fingerprint
-- the embed short-circuit probes with.

-- Fail fast instead of queueing a release behind a long-running lock holder
-- (a bulk rebuild's HNSW index build can run for hours): a lock wait beyond
-- this bound aborts the migration cleanly; stop the ingestion workers and
-- re-run it (after `migrate force` if the failure left the version dirty).
SET lock_timeout = '1min';

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

-- The staging table copies the generated column wholesale (it is created with
-- LIKE evidence_chunks INCLUDING GENERATED and renamed over the live table at
-- swap), so a staging table carrying the broken cast expression would
-- reinstate it on the live table the next time a bulk run resumes and swaps.
-- Drop staging ONLY in that case: a staging already on the fixed expression
-- belongs to a healthy in-flight rebuild whose embeddings are paid for, and
-- destroying it would abort that run and re-purchase every vector. The
-- leftover evidence_chunks_old table is inert (never swapped back), so it is
-- left alone here.
DO $$
DECLARE
    staging regclass := pg_catalog.to_regclass('evidence_chunks_staging');
BEGIN
    IF staging IS NULL THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_attribute a
        JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
        WHERE a.attrelid = staging
          AND a.attname = 'content_hash'
          AND a.attgenerated = 's'
          AND pg_catalog.pg_get_expr(d.adbin, d.adrelid) LIKE '%immutable_sha256%'
    ) THEN
        EXECUTE 'DROP TABLE evidence_chunks_staging';
    END IF;
END $$;

-- Regenerate the column under the fixed expression. IF EXISTS keeps the deploy
-- unwedged on a database where the broken column was already removed by an
-- out-of-band `ALTER TABLE ... DROP COLUMN` (raw SQL that leaves the migration
-- version at 18). Do NOT use 0018's down migration as the emergency unblock:
-- `migrate down 1` moves the version to 17, and the next `migrate up` replays
-- 0018's broken expression over whatever backslash content was ingested in the
-- meantime and wedges the deploy before 0019 can run. The ADD rewrites the
-- table and rebuilds every index on it (the HNSW and GIN indexes included)
-- under ACCESS EXCLUSIVE; the recompute itself is total (convert_to cannot
-- fail on any content), but on a corpus of production scale apply this in a
-- maintenance window with ingestion workers stopped - or reset the corpus
-- first, since bulk ingest rebuilds it anyway. The index is recreated under
-- its original name because the staging-swap code recreates it by that name.
ALTER TABLE evidence_chunks DROP COLUMN IF EXISTS content_hash;
ALTER TABLE evidence_chunks
    ADD COLUMN content_hash bytea
    GENERATED ALWAYS AS (immutable_sha256(content)) STORED;

CREATE INDEX evidence_chunks_source_content_hash
    ON evidence_chunks (source, content_hash);

-- The migrate run reuses one session for consecutive files; do not leak the
-- bound into later migrations.
RESET lock_timeout;
