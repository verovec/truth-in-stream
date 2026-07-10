# Ingestion resilience, source expansion, and fact-check search design

Date: 2026-07-10. Status: validated design; delivery tracked as four Linear card groups
(resilience, sources, search, docs). This spec is the umbrella: each card restates its own
acceptance criteria; this document records the shared reasoning, the current-state audit the
design responds to, and the dependency structure.

## 1. Goals

1. The ingestion pipeline runs near-endlessly: producers (crawlers) push self-contained
   messages to RabbitMQ on a schedule, consumers drain them into Postgres/pgvector, and the
   system self-heals from broker restarts, upstream throttling, partial failures, and crashes
   without losing data or requiring an operator.
2. The evidence corpus becomes the largest trustworthy French-politics-oriented database we
   can legally build: official statistics, parliamentary records, institutional publications,
   and claim-level fact-check corpora.
3. Live fact-check search stays fast and gets more accurate as the corpus grows to hundreds
   of GB: hybrid retrieval, tuned recall, fewer redundant embeddings, and a controlled
   embedding volume (the data structure stays lean).
4. The DeepSeek reasoner moves to the very end of the fact-check pipeline as the last-resort
   arbiter: if it is not at least 90% confident, the claim is reported unverifiable.

## 2. Current-state audit (2026-07-10)

### 2.1 Operational health

- Local: the dev stack was down; the compose `scheduler` service exists but every source is
  disabled by default (`SCHEDULE_<SOURCE>_ENABLED`) and `.env` sets no `SCHEDULE_*` vars, so
  even a running stack schedules nothing. `voting_records` was empty and the corpora were
  seed-sized. Backend `go test -race ./...` is green on main.
- Remote: unverifiable read-only — the gitignored `deploy/targets.json` is absent locally, so
  the account guard refuses every `scripts/ingest-host.sh ... status` call. Restoring that
  file from `deploy/targets.example.json` is a human action and a prerequisite for any remote
  pipeline verification.

### 2.2 Pipeline fragility audit (verified against main at e13f7f2)

Four producer-to-queue-to-consumer pipelines share one RabbitMQ transport (`internal/queue`):
wikipedia (`crawl.chunks`), stats (`embedding.jobs`), factcheck (`factcheck.claims`),
scrutins (`scrutins.votes`), all versioned `<base>.v<n>`, durable, priority-enabled,
publisher-confirmed, manually acked. Idempotency is upsert-key based everywhere. The
load-bearing gaps:

1. No AMQP reconnect anywhere: a broker restart, network blip, or the weekly Amazon MQ
   maintenance reboot ends every consumer loop (process exit) and fails producer runs;
   recovery relies solely on container restart policy.
2. No dead-letter queue: a message that exhausts its in-band attempt budget (default 5) is
   acked away with only an ERROR log — permanent, invisible data loss.
3. Re-enqueue failure path nacks the original without incrementing the attempt counter — a
   degraded broker redelivers the same job forever.
4. Single-instance broker in dev and prod defaults; the weekly maintenance reboot (Monday
   04:00 UTC) overlaps the default factcheck (04:00) and scrutins (04:30) crons.
5. Zero CloudWatch alarms on queue metrics; the metrics lambda and dashboard are gated off by
   default. A stalled consumer (backlog rising, consumers=0) alerts nobody.
6. The Wikipedia category crawl keeps no checkpoint, accumulates its BFS in memory, and
   aborts the whole run on the first extract/publish error; the fact-checkability gate fails
   open on error (diluting the corpus and spend).
7. Stats and factcheck fetchers have no 429/5xx retry or backoff (the MediaWiki client is the
   only upstream-polite one).
8. The embed worker's batch size is input-count capped but not token capped: an oversized
   batch permanently degrades to per-chunk embedding (128x request amplification).
9. Scrutins applies a multi-record write without a transaction (transient partial-vote
   window); the wiki delta sync embeds inline (not via the fleet) and re-does the entire
   change window if it fails before the final checkpoint write.
10. Consumers have no completion/failure alerting and a fully manual lifecycle: if the
    consumer host is off, producers durably fill queues that nothing drains.
11. `docs/ingestion-pipeline.md` still carries a stale "target — VER-74" status banner for
    long-shipped work and duplicated section numbers.

### 2.3 Search-path audit (verified against main at e13f7f2)

- Retrieval is dense-only: every store (`claims`, `evidence_chunks`, `political_claims`) is a
  single halfvec(1024) cosine HNSW; no lexical/FTS index, no reranker, no query expansion.
  Thresholds are static per corpus; `hnsw.ef_search` defaults to a fixed value with per-query
  tuning plumbing (`searchTuned`, iterative scan) currently exercised only by the opt-in BQ
  two-stage path (`EVIDENCE_BQ_MULTIPLIER`, default 0).
- `evidence_chunks` (VER-174) is the right substrate: source-extensible rows keyed
  `(source, external_id, chunk_index)` with jsonb provenance — a new source is data, not a
  migration. `SearchEvidenceChunks` already supports per-source filtering.
- The verify path multiplies per-unit cost (decompose LLM + per-claim embed + 1-2 ANN + verify
  LLM) under a tiny pool (concurrency 2, queue 4); the legacy path double-embeds each unit.
  The only live cache is a 30s exact-string claim cache; Redis snapshots are replay-only.
- `MATCH_*`, `PRECHECK_*`, `LIVE_*`, and `FACTCHECK_LANGUAGE` are not forwarded to the
  backend container by compose, so live tuning requires editing compose.
- DeepSeek today is both the default provider for all fast stages (`LLM_PROVIDER=deepseek`)
  and the second-pass reasoner (`deepseek-v4-pro`), which only re-judges evidence-based
  verdicts inside a confidence band [0.45, 0.8], after emission, and never runs for
  knowledge-basis or unverifiable verdicts. That is not a final fallback.

## 3. Design

### 3.1 Resilience (card group A)

Target properties: at-least-once delivery with dead-letter capture (never silent loss);
self-healing connections; resumable, partial-progress producers; autonomous drain; observable
and alarmed by default.

- A1 — Queue transport resilience. Reconnect-with-backoff on `NotifyClose` for consumers and
  producers (consumer loops survive broker restarts; producers retry through them); per-queue
  DLQs via dead-letter-exchange declaration; exhausted or malformed messages are parked, not
  dropped; the republish-failure path stops requeueing without burning an attempt. All
  behaviour covered by table-driven tests against the existing worker integration harness.
- A2 — Resumable producers and upstream politeness. Persistent crawl checkpoints (resume the
  category walk and page cursor), bounded error budget instead of abort-on-first-error, a
  shared retry/backoff HTTP helper (429/5xx + Retry-After) adopted by the stats and factcheck
  fetchers, and a fail-open/fail-closed knob for the fact-checkability gate.
- A3 — Worker hardening (after A1). Token-budget-aware embed batching with split-on-failure;
  transactional scrutins writes; delta sync publishing to the fleet with incremental
  checkpoints; consumer Slack notifications (start/finish/error/drop) mirroring producers.
- A4 — Observability and alarms (Terraform). Metrics lambda + dashboard on by default;
  CloudWatch alarms on backlog-growth-with-zero-consumers, DLQ depth > 0, and
  no-successful-run-in-24h per source, all wired to the alerts SNS topic; broker maintenance
  window moved off the producer cron slots. Applies stay human-gated.
- A5 — Hands-off ingestion loop (after A3). Consumers gain a drain-to-idle exit mode; the
  consumer host stops itself when all workers have idled out; scheduled producer runs use the
  existing stop-after semantics; one command reports end-to-end pipeline health (host states,
  queue depths, DLQ depths, corpus counts, last-run recency). Local autonomy documented:
  enabling `SCHEDULE_*` in `.env` makes the local stack continuously self-updating.

### 3.2 Source expansion (card group B)

Principles: official/institutional sources first; every connector publishes self-contained
queue messages consumed by the existing workers into `evidence_chunks` rows (new source =
new `source` value); one connector per protocol family, many datasets per connector; strict
legal guardrails.

Legal guardrails (apply to every source card):
- Store claim text + categorical verdict + source URL only for fact-check outlets; never bulk
  full-text of editorial articles. Repeated systematic extraction from one outlet's site is a
  French/EU sui generis database-right exposure — prefer official feeds and APIs.
- Respect licences: Etalab Licence Ouverte 2.0 (French public data), CC BY 4.0 (Eurostat,
  OECD, World Bank); IMF commercial reuse needs explicit permission — hold. Record the
  licence per source in `docs/fact-check-sources.md`.

Priority connectors (from the 2026-07 source research):
- B0 — Connector framework (after A2): a source-adapter registry so the scheduler and
  compose wiring are table-driven; a generic evidence job shape; shared pagination/backoff.
- B1 — SDMX connector: Eurostat (expanded datasets), ECB, OECD, Banque de France Webstat,
  INSEE Melodi/BDM where SDMX-compatible. One client, many endpoints.
- B2 — OpenDataSoft connector: DREES (health/social), DARES (labor), URSSAF (employment),
  plus the SSMSI crime/delinquency CSV bases. Identical API pattern shared across them.
- B3 — Parliament expansion: Assemblée nationale amendements, questions, comptes rendus
  (bulk XML/JSON dumps); Sénat dosleg + questions; Sénat scrutins joining the existing
  AN-only voting pipeline.
- B4 — Claim-corpus expansion (after A2, B0): broadened French-language Google Fact Check
  Tools queries; the DataCommons ClaimReview feed; a ClaimReview JSON-LD connector over an
  EFCSN/IFCN-derived outlet allowlist (claim + rating + URL only); a one-time ClaimsKG seed;
  an EDMO repository access spike.
- B5 — Phase-2 institutional sources (low priority): vie-publique discours metadata, Cour
  des comptes reports, HATVP declarations, Légifrance PISTE. PDF-heavy items reuse the
  existing document extraction path.

### 3.3 Search quality and performance (card group C)

- C5 — Retrieval eval gate first: extend the golden eval set with French political and
  statistical claims that stress lexical precision (numbers, named entities, dates), and make
  the eval + vectorbench comparison the merge gate for every retrieval-affecting card.
- C1 — Hybrid retrieval (after C5): French-configuration `tsvector` generated columns with
  unaccent + GIN on `claims` and `evidence_chunks`, reciprocal-rank-fusion of lexical and
  dense candidates, benchmark-gated so the eval set proves the gain before it ships.
- C2 — Tuning, caching, and call-count reduction (after C1 and C4): per-corpus thresholds and
  per-query `ef_search`/iterative-scan via the existing `searchTuned` plumbing; a semantic
  claim-verdict cache (embedding-similarity keyed, TTL) replacing the 30s exact-string cache;
  the legacy path's double embed collapsed to one; compose passthrough for `MATCH_*`,
  `PRECHECK_*`, `LIVE_*`, `FACTCHECK_LANGUAGE`.
- C3 — Embedding-volume control (after C2): content-hash and near-duplicate detection at the
  store boundary so re-crawls and boilerplate do not multiply vectors; per-source retention
  and pruning; a documented, benchmark-gated decision point for enabling the BQ two-stage
  search by default once corpus size warrants it. Keeps the "fewer, better embeddings"
  property as the corpus grows.
- C4 — DeepSeek final gate (independent, high priority — see 3.4).

### 3.4 DeepSeek as the terminal arbiter (C4)

Semantics change from "mid-band second opinion" to "end-of-pipeline last resort":

1. The reasoner runs after every other stage (gate, decompose, retrieval, curated borrow,
   LLM verifier, political two-axis verify) has produced its best verdict.
2. It triggers exactly when that best verdict is weak: verdict unverifiable, or confidence
   below a configurable floor — replacing the current [0.45, 0.8] band-and-evidence-only
   qualifier as the trigger policy (the band gate goes away; the citation guard stays).
3. Acceptance rule: the reasoner's verdict is adopted only if it is grounded (evidence basis
   with surviving citations) and its calibrated confidence is >= 0.90 (configurable,
   `FACTCHECK_FINAL_GATE_MIN_CONFIDENCE`). Anything less: the claim is emitted as
   unverifiable. "We prefer saying unverifiable over guessing."
4. The reasoner provider/model is decoupled from the fast-stage `LLM_PROVIDER` so the fast
   stages and the terminal arbiter can be operated and billed independently.
5. Political mode integration: when the two-axis path yields literal=unverifiable, the same
   terminal gate applies before emission; the credibility derivation is unchanged.
6. The upgrade re-emission mechanics (same claim id, cache overwrite, tally exclusion) are
   preserved.

## 4. Dependency structure

```
A1 -> A3 -> A5
A2 -> B0 -> {B1, B2, B3, B5}
{A2, B0} -> B4
C5 -> C1 -> C2 -> C3
C4 (independent)
C4 -> C2 (verify-path file overlap)
{A5, C3, B4} -> D1 (docs refresh)
```

A4 (Terraform alarms) is independent of the backend chain. D1 refreshes
`docs/ingestion-pipeline.md` (stale banner, renumbering, new resilience semantics),
`docs/fact-check-sources.md` (source inventory + licences), and `docs/configuration.md`
(new knobs) once the behaviour-changing cards have merged.

## 5. Out of scope / human actions

- Recreate `deploy/targets.json` from `deploy/targets.example.json` (blocked all remote
  verification on 2026-07-10). Any Terraform apply (alarms, broker changes) stays human-gated.
- Deciding to run the local scheduler continuously (cost: LLM gate + embedding spend) is an
  operator choice; A5 documents it.
- Multi-AZ broker for prod is noted in A4 as an operator decision, not auto-applied.
- No changes to the live transcription path, the political verdict model, or the documents
  pipeline beyond the integration points named above.
