-- name: InsertClaimChecks :batchexec
-- Telemetry rows are inserted in batches by the asynchronous recorder; the
-- table is append-only, so this is a plain insert with no conflict target.
INSERT INTO claim_checks (
    occurred_at, session_kind, locale, speaker, unit_text, claim_text,
    decision_path, skip_reason,
    retrieval_top, retrieval_candidates, retrieval_claim_hits, retrieval_evidence_hits,
    verdict, basis, literal, confidence, source,
    escalated, llm_calls, latency_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20);

-- name: CountClaimChecksBefore :one
-- The retention dry run: how many rows an apply would remove.
SELECT count(*) FROM claim_checks WHERE occurred_at < $1;

-- name: DeleteClaimChecksBefore :execrows
-- The retention sweep: rows older than the cutoff age out; the occurred_at
-- index serves the range delete.
DELETE FROM claim_checks WHERE occurred_at < $1;

-- name: ListClaimChecksSince :many
-- Dataset builds and tests read recent rows oldest-first; the occurred_at
-- index serves the range scan.
SELECT occurred_at, session_kind, locale, speaker, unit_text, claim_text,
       decision_path, skip_reason,
       retrieval_top, retrieval_candidates, retrieval_claim_hits, retrieval_evidence_hits,
       verdict, basis, literal, confidence, source,
       escalated, llm_calls, latency_ms
FROM claim_checks
WHERE occurred_at >= $1
ORDER BY occurred_at, id;
