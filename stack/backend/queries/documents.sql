-- name: CreateDocument :one
-- The id is caller-minted (the object key embeds it), unlike videos whose id
-- the database assigns.
INSERT INTO documents (id, title, object_key, content_type, size_bytes, status)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, title, object_key, content_type, size_bytes, page_count, status, analysis_status, analysis_error, sentences_total, sentences_processed, analyzed_at, analysis_runs, created_at, updated_at;

-- name: GetDocument :one
SELECT id, title, object_key, content_type, size_bytes, page_count, status, analysis_status, analysis_error, sentences_total, sentences_processed, analyzed_at, analysis_runs, created_at, updated_at
FROM documents
WHERE id = $1;

-- name: ListDocuments :many
-- Library rows, newest first, each with its verdict summary counts. The FILTER
-- counts read stored claims only; a document with no claims counts zero.
-- sqlc.embed keeps the row a real db.Document, so a future documents column
-- cannot silently drop out of the list mapping.
SELECT sqlc.embed(d),
       count(c.id) FILTER (WHERE c.verdict = 'credible') AS credible_claims,
       count(c.id) FILTER (WHERE c.verdict = 'disputed') AS disputed_claims
FROM documents d
LEFT JOIN document_claims c ON c.document_id = d.id
GROUP BY d.id
ORDER BY d.created_at DESC, d.id;

-- name: SetDocumentExtracted :one
-- Flip a pending document to ready with its extraction metadata. The status
-- guard makes extraction exactly-once: a non-pending document returns no row
-- and the store maps that to a conflict.
UPDATE documents
SET page_count = $2, sentences_total = $3, status = 'ready', updated_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING id, title, object_key, content_type, size_bytes, page_count, status, analysis_status, analysis_error, sentences_total, sentences_processed, analyzed_at, analysis_runs, created_at, updated_at;

-- name: InsertDocumentSentence :batchexec
INSERT INTO document_sentences (document_id, seq, page, text, occurrence)
VALUES ($1, $2, $3, $4, $5);

-- name: ListDocumentSentences :many
SELECT document_id, seq, page, text, occurrence, skip_reason
FROM document_sentences
WHERE document_id = $1
ORDER BY seq;

-- name: ListDocumentClaims :many
-- ordinal, not created_at, carries insertion order: an analysis run writes its
-- claims in one transaction, so their created_at values are identical.
SELECT id, document_id, sentence_seq, claim_id, text, status, source, verdict, basis, literal, flags, confidence, rationale, citations, created_at
FROM document_claims
WHERE document_id = $1
ORDER BY sentence_seq, ordinal;

-- name: DeleteDocument :execrows
-- Sentences and claims go with the document via ON DELETE CASCADE.
DELETE FROM documents
WHERE id = $1;

-- name: LockDocumentForAnalysis :one
-- Claim a ready document for a fresh analysis run: flip it to analysing (the
-- lock), zero the progress counter, and clear any prior error - all in one
-- guarded update. The guard admits a document that is ready and not already
-- analysing (so a none/complete/failed analysis re-runs, a concurrent run is
-- excluded). No row returned means the store resolves why (unknown, not ready,
-- or already analysing) and maps it to the right error. Prior claims and skip
-- reasons are NOT wiped here: each sentence's results are replaced as it is
-- reprocessed, so a run that fails partway keeps the previous run's verdicts for
-- the sentences it never reached instead of destroying them all up front.
UPDATE documents
SET analysis_status = 'analysing', sentences_processed = 0, analysis_error = '', updated_at = now()
WHERE id = $1 AND status = 'ready' AND analysis_status <> 'analysing'
RETURNING id, title, object_key, content_type, size_bytes, page_count, status, analysis_status, analysis_error, sentences_total, sentences_processed, analyzed_at, analysis_runs, created_at, updated_at;

-- name: DeleteDocumentSentenceClaims :exec
-- Remove one sentence's prior claims just before its fresh results are written,
-- so re-analysing a sentence replaces its verdicts atomically without wiping the
-- whole document up front.
DELETE FROM document_claims
WHERE document_id = $1 AND sentence_seq = $2;

-- name: BumpDocumentSentencesProcessed :exec
-- Advance the progress counter by one completed sentence. Progress is database
-- state, so it survives a page refresh mid-run.
UPDATE documents
SET sentences_processed = sentences_processed + 1, updated_at = now()
WHERE id = $1;

-- name: SetDocumentSentenceSkipReason :exec
-- Record why a sentence was not verified this run (not_a_claim / not_covered).
UPDATE document_sentences
SET skip_reason = $3
WHERE document_id = $1 AND seq = $2;

-- name: InsertDocumentClaim :batchexec
-- Persist one atomic claim's verdict. ordinal is assigned by the identity
-- column, preserving insertion order within the sentence.
INSERT INTO document_claims (document_id, sentence_seq, claim_id, text, status, source, verdict, basis, literal, flags, confidence, rationale, citations)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: CompleteDocumentAnalysis :exec
-- Terminal success: mark the run complete, stamp the completion time, and count
-- the run.
UPDATE documents
SET analysis_status = 'complete', analysis_error = '', analyzed_at = now(), analysis_runs = analysis_runs + 1, updated_at = now()
WHERE id = $1;

-- name: FailDocumentAnalysis :exec
-- Terminal failure: record the reason and flip the run to failed so the admin
-- can reanalyse.
UPDATE documents
SET analysis_status = 'failed', analysis_error = $2, updated_at = now()
WHERE id = $1;

-- name: RecoverInterruptedAnalyses :many
-- Startup recovery: any document left analysing when the process died is flipped
-- to failed with a clear reason. Returns the recovered ids for logging.
UPDATE documents
SET analysis_status = 'failed', analysis_error = 'interrupted by restart', updated_at = now()
WHERE analysis_status = 'analysing'
RETURNING id;
