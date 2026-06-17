-- name: UpsertPoliticalClaim :exec
-- The fact-check-archive crawler writes a curated claim with its embedding in one
-- statement, so a row is never visible to ANN search without its matching vector.
-- The embedding is the freshly computed one, so a re-crawl rewrites the same row
-- idempotently. The embedding is bound text-form (::halfvec via the pgvector
-- Valuer), never binary COPY.
INSERT INTO political_claims (
    id, content, literal_verdict, flags, source_name, source_url, quoted_span, outlet, checked_at, embedding, synced_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
ON CONFLICT (id) DO UPDATE
    SET content = EXCLUDED.content,
        literal_verdict = EXCLUDED.literal_verdict,
        flags = EXCLUDED.flags,
        source_name = EXCLUDED.source_name,
        source_url = EXCLUDED.source_url,
        quoted_span = EXCLUDED.quoted_span,
        outlet = EXCLUDED.outlet,
        checked_at = EXCLUDED.checked_at,
        embedding = EXCLUDED.embedding,
        synced_at = now();

-- name: SearchPoliticalClaims :many
-- Approximate nearest-neighbor retrieval over the curated political claim DB,
-- mirroring SearchClaims: the fast path borrows an instant verdict for a repeated
-- talking point. query_embedding is referenced twice but sqlc collapses it to one
-- parameter, so the HNSW index drives the ORDER BY.
SELECT id, content, literal_verdict, flags, source_name, source_url, quoted_span, outlet, checked_at,
       (embedding <=> sqlc.arg(query_embedding))::float8 AS distance
FROM political_claims
ORDER BY embedding <=> sqlc.arg(query_embedding)
LIMIT sqlc.arg(result_limit);

-- name: UpsertVotingRecord :exec
-- Scrutins ingest writes one recorded position per person per scrutin. Re-running
-- the ingest rewrites the same row, so a bulk re-run is idempotent.
INSERT INTO voting_records (
    person_id, person_name, chamber, scrutin_id, bill_title, voted_on, position, source_url, synced_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
ON CONFLICT (person_id, scrutin_id) DO UPDATE
    SET person_name = EXCLUDED.person_name,
        chamber = EXCLUDED.chamber,
        bill_title = EXCLUDED.bill_title,
        voted_on = EXCLUDED.voted_on,
        position = EXCLUDED.position,
        source_url = EXCLUDED.source_url,
        synced_at = now();

-- name: LookupVotingRecords :many
-- The voting adapter answers "how did person X vote on bill Y around date Z". The
-- predicate order matches voting_records_person_bill_date_idx. The date is an
-- exact match on the recorded scrutin date; a caller resolves the scrutin date
-- before lookup.
SELECT person_id, person_name, chamber, scrutin_id, bill_title, voted_on, position, source_url
FROM voting_records
WHERE person_id = sqlc.arg(person_id)
  AND bill_title = sqlc.arg(bill_title)
  AND voted_on = sqlc.arg(voted_on)
ORDER BY scrutin_id;
