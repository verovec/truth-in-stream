-- VER-229: per-claim pipeline telemetry.
--
-- claim_checks is the append-only analytical record of what each pipeline
-- stage decided for one claim occurrence and what it cost. It exists so
-- thresholds (gate bands, similarity floors, escalation triggers) are
-- calibrated from real traffic and the local-classifier training sets can be
-- mined from in-domain rows. It is never read by viewer-facing paths, carries
-- no foreign keys (an analytics row must survive its video's deletion), and
-- deliberately no jsonb: everything a query filters or aggregates on is a
-- typed column, per the schema's standing rule. Vocabulary (decision_path,
-- verdict axes) is enforced in Go, matching the evidence-chunk kind
-- convention, so a future stage is a new constant rather than a migration.
--
-- Writes are asynchronous and lossy-by-design upstream; the table only ever
-- receives inserts and the retention sweep's deletes.

CREATE TABLE claim_checks (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at             timestamptz NOT NULL DEFAULT now(),
    session_kind            text NOT NULL DEFAULT '',
    locale                  text NOT NULL DEFAULT '',
    speaker                 text NOT NULL DEFAULT '',
    unit_text               text NOT NULL DEFAULT '',
    claim_text              text NOT NULL DEFAULT '',
    decision_path           text NOT NULL,
    skip_reason             text NOT NULL DEFAULT '',
    retrieval_top           float8 NOT NULL DEFAULT 0,
    retrieval_candidates    integer NOT NULL DEFAULT 0,
    retrieval_claim_hits    integer NOT NULL DEFAULT 0,
    retrieval_evidence_hits integer NOT NULL DEFAULT 0,
    verdict                 text NOT NULL DEFAULT '',
    basis                   text NOT NULL DEFAULT '',
    literal                 text NOT NULL DEFAULT '',
    confidence              float8 NOT NULL DEFAULT 0,
    source                  text NOT NULL DEFAULT '',
    escalated               boolean NOT NULL DEFAULT false,
    llm_calls               integer NOT NULL DEFAULT 0,
    latency_ms              bigint NOT NULL DEFAULT 0
);

-- The two operator read patterns: rates over time (generative calls, verdict
-- mix) and distribution by decision path over time. occurred_at leads both so
-- the retention sweep's range delete uses them too.
CREATE INDEX claim_checks_occurred_at ON claim_checks (occurred_at DESC);
CREATE INDEX claim_checks_path_occurred_at ON claim_checks (decision_path, occurred_at DESC);
