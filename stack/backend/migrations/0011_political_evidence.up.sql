-- Political fact-checking evidence layer (epic VER-93). Two stores the verifier
-- checks against, additive and inert until FACTCHECK_POLITICAL wires them in:
--   1. political_claims - a curated, pre-checked claim record matched semantically
--      (HNSW cosine over a halfvec(1024) embedding), carrying a two-axis verdict
--      (literal accuracy + orthogonal manipulation flags) and its primary source.
--   2. voting_records   - structured Assemblee Nationale / Senat scrutins, queried
--      relationally by (person, bill, date), never by cosine.
-- These are independent of the existing claims/wiki corpora; nothing reads them
-- until the political verify path lands.

-- A curated, pre-checked political claim and the embedding used to match spoken
-- segments against it. literal_verdict is the objective proposition check; flags
-- is an orthogonal set of context/manipulation tags (so "accurate, but
-- cherry-picked timeframe" is expressible). The primary source name/url and the
-- exact quoted span are stored so the UI can always show its work. outlet is the
-- fact-check outlet the record was sourced from; checked_at is when that outlet
-- published its check.
CREATE TABLE political_claims (
    id             text PRIMARY KEY,
    content        text NOT NULL,
    literal_verdict text NOT NULL CHECK (literal_verdict IN ('accurate', 'inaccurate', 'unverifiable')),
    -- The array is constrained to the known flag set just as the scalar verdict is,
    -- so the DB is a second line of defense against any writer that bypasses the
    -- Go-side validation. An empty array trivially satisfies the subset check.
    flags          text[] NOT NULL DEFAULT '{}'
        CHECK (flags <@ ARRAY['missing-context', 'cherry-picked', 'outdated', 'misattributed', 'misleading-causation']::text[]),
    source_name    text NOT NULL,
    source_url     text NOT NULL,
    quoted_span    text NOT NULL DEFAULT '',
    outlet         text NOT NULL,
    checked_at     timestamptz,
    embedding      halfvec(1024) NOT NULL,
    synced_at      timestamptz NOT NULL DEFAULT now()
);

-- Same HNSW parameters as claims_embedding_hnsw / wiki_chunks_embedding_hnsw so
-- query-time recall tuning (hnsw.ef_search) behaves identically across every
-- vector store. Plain CREATE INDEX is correct because the table is empty at
-- creation; the crawler upserts each row with its embedding in one statement
-- (text-form ::halfvec, never binary COPY), so the index absorbs inserts
-- incrementally.
CREATE INDEX political_claims_embedding_hnsw
    ON political_claims USING hnsw (embedding halfvec_cosine_ops)
    WITH (m = 16, ef_construction = 200);

-- A single deputy's or senator's recorded position on one dated scrutin. The
-- voting adapter answers "did X vote for/against bill Y" by an exact relational
-- lookup, so there is no embedding here. position is constrained to the four
-- recorded outcomes an open-data scrutin reports. (person_id, scrutin_id) is the
-- natural key: one recorded position per person per scrutin, making re-ingest
-- idempotent.
CREATE TABLE voting_records (
    person_id    text NOT NULL,
    person_name  text NOT NULL,
    chamber      text NOT NULL CHECK (chamber IN ('assemblee', 'senat')),
    scrutin_id   text NOT NULL,
    bill_title   text NOT NULL,
    voted_on     date NOT NULL,
    position     text NOT NULL CHECK (position IN ('for', 'against', 'abstain', 'absent')),
    source_url   text NOT NULL,
    synced_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (person_id, scrutin_id)
);

-- The voting adapter looks a record up by who voted, on which bill, and when, so
-- the lookup column order matches the query predicate (person, bill, date).
CREATE INDEX voting_records_person_bill_date_idx
    ON voting_records (person_id, bill_title, voted_on);
