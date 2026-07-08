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
SELECT d.id, d.title, d.object_key, d.content_type, d.size_bytes, d.page_count, d.status, d.analysis_status, d.analysis_error, d.sentences_total, d.sentences_processed, d.analyzed_at, d.analysis_runs, d.created_at, d.updated_at,
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
SELECT id, document_id, sentence_seq, claim_id, text, status, source, verdict, basis, literal, flags, confidence, rationale, citations, created_at
FROM document_claims
WHERE document_id = $1
ORDER BY sentence_seq, created_at, id;

-- name: DeleteDocument :execrows
-- Sentences and claims go with the document via ON DELETE CASCADE.
DELETE FROM documents
WHERE id = $1;
