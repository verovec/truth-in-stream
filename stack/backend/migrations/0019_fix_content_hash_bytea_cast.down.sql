-- Restore the 0018 expression this migration replaced. Rolling back is an
-- explicitly destructive operation with two constraints inherent to the
-- cast-based fingerprint:
--
--  - A staging or _old table built after 0019 depends on immutable_sha256
--    through its LIKE-copied generated column, which would make the DROP
--    FUNCTION below fail. Both are transient rebuild artifacts (the next bulk
--    run recreates staging from the live table), so they are dropped first;
--    an in-flight bulk rebuild does not survive this rollback.
--
--  - Re-adding the cast-based column re-parses every stored content as a bytea
--    escape literal, so the rewrite aborts with 22P02 if the corpus holds
--    content only the fixed expression admits (lone-backslash sequences). That
--    abort is correct: such rows could never have existed under 0018, and
--    silently discarding them to force the rollback would lose data. Delete
--    the offending rows or reset the corpus before rolling back.

-- Fail fast instead of queueing behind a long-running lock holder; see the up
-- migration. RESET at the end keeps the bound out of later files in the run.
SET lock_timeout = '1min';

DROP TABLE IF EXISTS evidence_chunks_staging;
DROP TABLE IF EXISTS evidence_chunks_old;

ALTER TABLE evidence_chunks DROP COLUMN IF EXISTS content_hash;
ALTER TABLE evidence_chunks
    ADD COLUMN content_hash bytea
    GENERATED ALWAYS AS (sha256(content::bytea)) STORED;

CREATE INDEX evidence_chunks_source_content_hash
    ON evidence_chunks (source, content_hash);

DROP FUNCTION IF EXISTS immutable_sha256(text);

RESET lock_timeout;
