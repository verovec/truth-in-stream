# Fact-check sources — how each one works

This document explains where the fact-checker gets its evidence and how each
source produces a verdict. It also covers what the user sees today vs. what a
"show the source" / debug-mode feature would add.

> **Quick correction to common assumptions**
> - There is **no Google fact-check**. "Google" exists only as an optional
>   *LLM provider* (Gemini, `LLM_PROVIDER=gemini`) — it is the brain that reads
>   evidence, not a source of evidence. There is no Google Fact Check Tools API.
> - There are **more sources than the obvious four.** Besides Assemblee,
>   Wikipedia, INSEE and the LLM, the system also uses **Eurostat**,
>   **Press/Attribution** and **general Web search** (both Brave Search).

---

## The big picture (how a claim gets checked)

```
Live transcript
  |
  v
Check-worthiness gate        -- is this even a factual claim?
  |
  v
Decompose into atomic claims -- one statement -> N checkable claims
  |
  |-- Curated fast-path: does this nearly match a claim already in our DB?
  |      yes -> borrow that verdict instantly (source = "curated", no LLM call)
  |      no, continue below
  |
  v
Classify the claim's TYPE    -- statistic? voting record? quote? causal?
  |
  v
ROUTE to the right source    -- pick the authoritative source for that type
  |
  |- Statistic         -> INSEE / Eurostat (live API)
  |- Voting record     -> voting_records table (Assemblee / Senat)
  |- Attribution/quote -> Press search (Brave)
  |- Causal/compare/?  -> Web search (Brave)
  |- (thin results)    -> Web search fallback
  |
  v
RETRIEVE evidence passages   -- each passage carries a stable evidence_id
  |
  v
VERIFY with the LLM          -- reads passages, returns a grounded verdict
  |                             + citations (each citation points at an evidence_id)
  v
Emit per-claim result frame  -- verdict + confidence + cited evidence -> frontend
```

Two feature flags gate this:

- `FACTCHECK_VERIFY_PATH` — turns on the retrieve-then-verify path (decompose,
  route, retrieve, verify). Off = the legacy gate-and-match path.
- `FACTCHECK_POLITICAL` — turns on the two-axis political verifier (a *literal*
  verdict **plus** *manipulation flags* like cherry-picked / missing-context).

Key code:
- `internal/service/verify_path.go` — the lifecycle (decompose, retrieve, verify, emit)
- `internal/service/route.go` — picks the source for each claim type
- `internal/verify/verify.go` + `internal/verify/political.go` — the LLM verifiers
- `internal/source/*` — one adapter per evidence source
- `internal/handler/live.go` — serializes the result frame to the WebSocket

---

## 1. Assemblee Nationale fact-check (voting records)

**What it answers:** "Did deputy X vote for/against bill Y?"

- **Where the data comes from:** official Assemblee open-data scrutins
  (`data.assemblee-nationale.fr/.../Scrutins.json.zip`, Etalab open licence).
- **Ingestion:** `cmd/scrutinsingest/main.go` bulk-loads the per-scrutin JSON.
- **Storage:** the `voting_records` table
  (`migrations/0011_political_evidence.up.sql`). Each row =
  `person_name`, `chamber` (assemblee | senat), `bill_title`, `voted_on`,
  `position` (for | against | abstain | absent), `source_url`. Natural key
  `(person_id, scrutin_id)` so re-ingest is idempotent.
- **Retrieval (NOT vector search):** an exact relational lookup by person +
  bill + date — `internal/source/voting/voting.go` calls
  `store.LookupVotingRecords(...)`.
- **Into the verdict:** each matching row becomes an evidence passage like
  *"X a vote pour le scrutin '...' (Assemblee nationale, date)"*, with
  `evidence_id = voting:{scrutin_id}:{index}` and the official scrutin URL.

This is the most authoritative source: it's a direct record lookup, not a
similarity guess.

---

## 2. Wikipedia fact-check (encyclopedic background)

**What it answers:** general factual/background claims with no better structured source.

- **Where the data comes from:** a MediaWiki XML dump, one corpus per
  environment (`WIKI_CORPUS`). Article lead sections are chunked.
- **Storage:** the `wiki_chunks` table (`migrations/0004_wiki_chunks.up.sql`):
  `title`, `url`, `content`, and an `embedding` (`halfvec(1024)`, HNSW cosine).
- **Retrieval (vector search):** the claim is embedded and the nearest chunks
  are pulled by cosine similarity (ANN). Each becomes an evidence passage with
  its article title + URL attached (`evidence_id = wiki:{page_id}:{chunk_index}`).
- **Into the verdict:** Wikipedia is the general-knowledge / fallback evidence —
  it grounds claims that aren't a statistic, vote, or quote.

---

## 3. INSEE fact-check (French statistics)

**What it answers:** French economic/demographic statistics ("unemployment was X%").

- **Where the data comes from:** the **live** INSEE BDM SDMX API
  (`bdm.insee.fr/series/sdmx/data/SERIES_BDM`, keyless). **No database** — it is
  queried at check time.
- **Retrieval:** `internal/source/stats/insee.go` does an HTTP GET for the
  relevant series (IDBANK), parses SDMX-ML, and returns the time-series
  observations. `evidence_id = insee:{IDBANK}:{index}`.
- **Into the verdict:** the verifier sees the **whole series**, not just one
  number, so it can catch cherry-picking (e.g. quoting a peak out of context).
- **Sibling source — Eurostat:** `internal/source/stats/eurostat.go` does the
  same for EU-wide stats via the Eurostat JSON-Stat API. INSEE + Eurostat are
  packed together and both serve the "statistic" claim type.

---

## 3b. SDMX connector — ECB, OECD, expanded Eurostat (macro backbone)

**What it answers:** the macro-economic backbone of French/EU fiscal and monetary
debate — inflation, public debt, unemployment, interest rates.

- **Where the data comes from:** one generic SDMX 2.1 REST client
  (`internal/source/sdmx`) fetches SDMX-CSV from several institutions and renders
  each observation into a French evidence passage the embedding fleet stores in
  `evidence_chunks`. Adding an institution is endpoint configuration plus a curated
  series list, not a new pipeline. The producer is `cmd/sdmxcrawl`; it upserts the
  passages un-embedded and publishes one embed job each to `embedding.jobs`, drained
  by the existing `embedworker` (same bulk-into-live path as `statsingest`).
- **Institutions and corpora (each a distinct `evidence_chunks.source` so the
  publisher is identifiable):**
  - **Eurostat** (`eurostat` corpus, expanded macro series: HICP inflation, general
    government gross debt, unemployment) — anonymous SDMX-CSV.
    Licence: **CC BY 4.0**, attribution "Source: Eurostat".
  - **ECB / BCE** (`ecb` corpus: euro-area HICP, French 10-year sovereign yield) —
    anonymous SDMX-CSV. Licence: **ESCB reuse policy** — free reuse quoting
    "Source: ECB", data/metadata unmodified; no reuse right over third-party data
    the ECB merely redistributes.
  - **OECD / OCDE** (`oecd` corpus: harmonised unemployment) — anonymous SDMX-CSV,
    **documented 60 requests/hour** (the client throttles). Licence: **CC BY 4.0**
    (OECD content since 1 July 2024).
- **Idempotency & robustness:** rows key on `(source, external_id, chunk_index)`
  derived from `(dataset, series key, period)`, so a refreshed observation updates
  its chunk (and re-embeds) instead of duplicating; upstream 429/5xx are backed off
  and retried by the shared `httpx` retry helper, and each institution is isolated
  in the producer (one provider's failure never blocks the others).
- **Deferred — Banque de France Webstat:** listed in the source plan but **not yet
  wired**. Its SDMX service sits behind the IBM API Connect developer portal and
  needs a registered account + `X-IBM-Client-Id` API key, and the exact endpoint
  template could not be confirmed without that account. The generic client already
  supports a gateway auth header (`ClientIDHeader`/`ClientID`), so Banque de France
  becomes a config-plus-secret addition once an operator registers and confirms the
  live OpenAPI spec. Its licence is **Etalab Licence Ouverte v2.0**. IMF is likewise
  excluded until its commercial-reuse terms are cleared.
- **Series keys:** the SDMX-CSV wire format (parsed by resolving the invariant
  `TIME_PERIOD`/`OBS_VALUE` columns by header name) is covered by fixtures; the exact
  curated series keys were verified July 2026 against each portal's DSD and are
  re-confirmed on the first live dev-account run (a stale key surfaces as an empty
  series in the run log and is fixed by editing the key in
  `internal/source/sdmx/catalog.go`). Note: the ECB `ICP` (HICP) dataflow was
  slated for replacement in Feb 2026, and euro-area HICP is also ingested from
  Eurostat directly, so inflation evidence is not solely dependent on it.
## 3c. OpenDataSoft & SSMSI statistics (ingested corpus)

**What it answers:** French social-policy battleground figures — health-system
financing (DREES), labor market (DARES), private-sector employment (URSSAF), and
recorded crime/delinquency (SSMSI).

- **Where the data comes from:** four keyless official portals, each ingested on
  demand into `evidence_chunks` (not live-queried) and rendered into self-contained
  French passages by the shared stats foundation (`internal/stats`), exactly like
  the interior-ministry immigration CSVs:
  - **DREES** — `data.drees.solidarites-sante.gouv.fr`, OpenDataSoft **Explore API
    v2.1**. Corpus `drees`.
  - **DARES** — `data.dares.travail-emploi.gouv.fr`, same Explore API v2.1. Corpus
    `dares`.
  - **URSSAF** — `open.urssaf.fr`, same Explore API v2.1. Corpus `urssaf`.
  - **SSMSI** — the délinquance départementale/régionale CSV bases on
    `data.gouv.fr` (Service statistique ministériel de la sécurité intérieure),
    resolved through the data.gouv.fr dataset API. Corpus `ssmsi`.
- **Ingestion:** `cmd/odsingest` sweeps the curated datasets, upserts un-embedded
  passages on the stable `(series, period)` provenance key, and publishes embedding
  jobs to the shared `embedding.jobs` queue the `embedworker` fleet drains — the
  same bulk-into-live path as `stats`, so re-runs are idempotent and updates
  supersede rather than duplicate. Adapters: `internal/stats/ods` (one Explore API
  client covering the three portals as configuration) and `internal/stats/ssmsi`
  (the CSV fetcher).
- **Operating it:** host-only, like `stats`. Run on the EC2 ingestion hosts with
  `ENVIRONMENT=dev scripts/ingest-host.sh crawler ods up` (producer) and
  `… consumer ods up` (the shared embedworker drains it) — the source is picked up
  automatically from the connector registry manifest, no host-script edit.
- **Licence & terms:** all four sources are French public data under the **Etalab
  Licence Ouverte 2.0** (open reuse with attribution). Attribution — the publisher
  (DREES/DARES/URSSAF/SSMSI), dataset code, and a resolvable source URL — is carried
  on every passage. The OpenDataSoft public datasets are keyless (anonymous access
  is per-portal rate-limited; the client backs off on HTTP 429), so no API key or
  secret is required.

---

## 4. LLM fact-check (the verifier — Claude or Gemini)

This is not a *source*; it's the **judge** that reads the retrieved evidence and
produces the verdict. Evidence comes from sources 1-3 and 5; the LLM decides.

- **Provider:** chosen by `LLM_PROVIDER` — Anthropic Claude (default,
  `claude-haiku-4-5`) or Google **Gemini** (`LLM_PROVIDER=gemini`). This is the
  only place "Google" appears. Factory: `internal/llm/factory.go`.
- **Two verifiers:**
  - `verify/verify.go` — credibility axis: `credible | disputed | unverifiable`.
  - `verify/political.go` (behind `FACTCHECK_POLITICAL`) — two axes:
    1. **literal:** `accurate | inaccurate | unverifiable`
    2. **flags:** any of `missing-context, cherry-picked, outdated,
       misattributed, misleading-causation`
- **Grounding guard:** the LLM must return citations as `evidence_id` +
  `quoted_span`, and a deterministic check (`ValidateCitations` /
  `ValidatePoliticalCitations`) **rejects any citation whose quote isn't an exact
  substring of a real passage.** Hallucinated citations can't survive.
- **Knowledge cap:** a verdict the LLM gives from its own knowledge (no evidence)
  is confidence-capped at 0.6, so ungrounded answers can't look certain.

---

## 5. Google fact-check — as an LLM provider, not a search source

"Google" is not a live web-search *source* in this codebase (that is Brave, see
below). It does appear two other ways: as the **Gemini LLM provider**, and — via
the **Google Fact Check Markup Tool** — as the origin of the ClaimReview markup
the curated claim corpus ingests offline (see the fact-check archive sources:
the Google Fact Check Tools API path and the **DataCommons ClaimReview feed** in
section 7). Those feed `political_claims` ahead of time; they are not queried at
check time. The things named "Google" at *check* time:

- **Gemini as LLM provider** (see section 4) — reads evidence, isn't a source.
- **Web search**, which is **Brave Search**, not Google
  (`internal/source/websearch/websearch.go`). It's the fallback for causal /
  comparative / untyped claims and when a preferred source returns too little.
- **Press/Attribution search**, also Brave
  (`internal/source/press/press.go`) — used to verify quotes ("X said Y").

The Google **Fact Check Tools API** path (the curated-corpus ingestion, not a
check-time source) is section 8; the other claim-level corpus paths are sections
7, 9, and 10.

---

## 6. TV live channels (capture inputs, not evidence)

These are **inputs to fact-check**, not evidence sources: the `tvcapture` worker records these
channels and feeds their audio into the same live pipeline as an imported video (the evidence still
comes from sources 1–5 above). The seed registry lives in
`internal/seed/tv_channel.go`; every channel ships **disabled**, with archiving **armed**, so an
operator turns each on deliberately. See the [Live TV capture runbook](tv-live.md) for the full flow.

TV capture is scoped to **free, non-DRM** sources. DRM commercial broadcasters (TF1, France 2, M6)
are deliberately **out of scope** and not in the registry. The registry has two classes with distinct
licence postures:

| Channel | Kind | `source_ref` | Licence / legal posture |
|---------|------|--------------|-------------------------|
| franceinfo | YouTube live | `youtube.com/franceinfo/live` | Official embed sanctioned; **stream-ripping/archiving conflicts with YouTube ToS** |
| France 24 (FR) | YouTube live | `youtube.com/@FRANCE24/live` | same YouTube ToS posture |
| BFMTV | YouTube live | `youtube.com/@BFMTV/live` | same |
| Euronews (FR) | YouTube live | `youtube.com/c/euronewsfr/live` | same |
| LCP | YouTube live | `youtube.com/@LCP/live` | same |
| Public Sénat | YouTube live | `youtube.com/@publicsenat/live` | same |
| CNEWS | YouTube live | `youtube.com/@CNEWSofficiel/live` | same |
| LCI | YouTube live | `youtube.com/@LCI/live` | same |
| Assemblée nationale | HLS portal | `videos.assemblee-nationale.fr/direct` | Public-broadcast constitutional mandate + Etalab precedent — **cleanest to archive** |
| Sénat | HLS portal | `videos.senat.fr/direct` | same parliamentary posture |

- **YouTube class:** embedding the official live is fine, but archiving it conflicts with YouTube's
  Terms of Service, so archiving is **per-channel opt-in** and paired with **short retention** (the
  recording is a short-lived working copy for analysis, not a rebroadcast archive).
- **Parliamentary class:** public-broadcast under a constitutional mandate, aligned with the Etalab
  open-data precedent and DRM-free HLS — the cleanest sources to archive.

---

## 7. Parliament open data (Assemblee nationale + Senat)

**What it answers:** what parliament actually did and said - an amendment's fate
and content ("cet amendement a ete rejete"), a written question and whether the
government answered it ("le gouvernement n'a jamais repondu sur Z"), a debate
excerpt, a legislative dossier, and how a senator voted on a named scrutin
("le senateur X a vote contre Y").

**One producer, six datasets.** `internal/source/parliament` +
`cmd/parliamentcrawl` ingest six open-data datasets, selected by
`PARLIAMENT_DATASET`. Every run does a conditional GET (persisted
ETag/Last-Modified) to skip an unchanged dump, then diffs the dump against a
**per-dataset manifest** that fingerprints each record, so a daily run republishes
only new or changed records (not the whole corpus). Older AN legislatures are an
explicit backfill (`PARLIAMENT_LEGISLATURE=16`); Senat scrutins volume is bounded
by `PARLIAMENT_SINCE_YEAR`; `PARLIAMENT_MAX_ITEMS` caps any run. Textual datasets
render attributed French passages chunked to the corpus convention and publish the
generic `connector.EvidenceJob` to `evidence.chunks`, drained by `cmd/evidenceworker`
(`internal/evidencejob`) into `evidence_chunks`. The voting dataset publishes a
chamber-aware scrutins job to `scrutins.votes`, drained by the existing
`scrutinsworker` (`internal/scrutinsjob` now dispatches by chamber) into
`voting_records` with `chamber=senat`.

| Dataset | Source id | Real dump | Target | `evidence_id` / record |
|---------|-----------|-----------|--------|------------------------|
| AN amendements | `an-amendements` | `.../repository/{leg}/loi/amendements_div_legis/Amendements.json.zip` (JSON) | evidence | `an-amendements:{uid}:{i}` |
| AN questions ecrites | `an-questions` | `.../repository/{leg}/questions/questions_ecrites/Questions_ecrites.json.zip` (JSON) | evidence | `an-questions:{uid}:{i}` |
| AN comptes rendus | `an-comptesrendus` | `.../repository/{leg}/vp/syceronbrut/syseron.xml.zip` (XML) | evidence | `an-comptesrendus:{uid}:{i}` |
| Senat questions | `senat-questions` | `data.senat.fr/data/questions/questions-depuis-un-an.csv` (CSV, Latin-1) | evidence | `senat-questions:{ref}:{i}` |
| Senat dosleg | `senat-dosleg` | `data.senat.fr/data/dosleg/dosleg.zip` -> `dosleg.sql`, `loi`+`typloi` tables | evidence | `senat-dosleg:{loicod}:{i}` |
| Senat scrutins | `senat-scrutins` | same `dosleg.sql`, `scr`+`votsen`+`posvot`+`auteur` tables joined | `voting_records` (chamber=senat) | `voting:senat-{sesann}-{scrnum}:{i}` |

Each parser was written against a **real downloaded sample** captured as a fixture
(`internal/source/parliament/testdata/*`); the Senat scrutins/dosleg fixtures are
verbatim excerpts of the official `dosleg.sql` COPY blocks. The Senat roll-call
votes ARE published in machine-readable form - inside the official `dosleg` PostgreSQL
dump (`scr` = scrutins, `votsen` = per-senator votes, `posvot` = position labels,
`auteur` = senators), joined by the streaming COPY reader in `pgdump.go`. The Senat
scrutin page (`www.senat.fr/scrutin-public/{year}/scr{year}-{num}.html`) is the
human-facing provenance link but carries no machine-readable export, so the dump is
the canonical source (the third-party NosSenateurs.fr mirror is deliberately not used).

**Licences.**
- Assemblee nationale (amendements, questions, comptes rendus): **Licence Ouverte /
  Open Licence version 2.0** (Etalab). Attribution: *"Source : Assemblee nationale -
  data.assemblee-nationale.fr, Licence Ouverte / Open Licence version 2.0"*.
- Senat (questions, dosleg, scrutins): the **Senat open-data licence**
  (`data.senat.fr/licence/`). Attribution: *"Source : Senat - data.senat.fr"*.

**Cloud parity.** All six run through the framework registry: one
`docker-compose.ingest.yml` `parliamentcrawl` service (dataset via
`PARLIAMENT_DATASET`), a per-dataset state file on the `parliament-state` volume,
and the manifest resolution the host script reads
(`ENVIRONMENT=dev scripts/ingest-host.sh crawler <source> up|status|down`). The
delivery e2e verifies manifest/compose/host-script resolution per dataset; a real
dev-account run additionally needs `deploy/targets.json` (gitignored on this public
repo) present on the operator's machine.
## 8. DataCommons ClaimReview feed (curated claim corpus)

**What it answers:** a talking point already fact-checked by a vetted French
outlet — the live fast-path borrows the outlet's verdict instantly instead of
re-reasoning it.

- **Where the data comes from:** the **DataCommons ClaimReview data feed**
  (`storage.googleapis.com/datacommons-feeds/claimreview/latest/data.json`), the
  aggregated, schema.org-standardized markup created through the Google Fact
  Check Markup Tool and refreshed daily. It is a **public, keyless** object, so
  the producer declares **no Secrets Manager entry and needs no per-source
  Terraform** (unlike the Google Fact Check Tools API path in section 5). Producer:
  `internal/datacommons` via `cmd/datacommonscrawl`.
- **The pipeline:** it is the **redundant, non-API path** onto the same corpus as
  the Google-API `factcheck` source. Both publish self-contained
  `factcheckjob.ClaimJob` bodies to the versioned `factcheck.claims` queue, and
  the existing `factcheckworker` embeds and upserts each into `political_claims`.
  Scheduled daily at **05:00 UTC** (`SCHEDULE_DATACOMMONS_*`), on-demand on the
  ingestion hosts via `scripts/ingest-host.sh crawler|consumer datacommons`.
- **French selection by outlet allowlist:** the feed carries **no per-record
  language tag**, so the French subset is selected by an author-URL allowlist
  (`DATACOMMONS_OUTLET_ALLOWLIST`, default AFP Factuel, Les Décodeurs / Le Monde,
  franceinfo Vrai ou Fake, 20 Minutes, Libération, France 24 Les Observateurs).
  Setting it empty ingests every outlet; `DATACOMMONS_MAX_ITEMS` caps a run.
- **Rating normalisation (table-driven, conservative):** the outlet's textual
  rating (`reviewRating.alternateName`, e.g. "Faux"/"Plutôt vrai") is folded
  through the shared French verdict table (`internal/factcheckarchive`); if it
  does not map, the numeric scale (`ratingValue`/`bestRating`/`worstRating`, where
  a lower value is more false) is tried; only the clearly-false and clearly-true
  ends map, and anything else lands as **`unverifiable` rather than a guess**.
- **Dedup:** the claim ID is the **review URL**, and the worker's upsert is keyed
  on it — so a claim reviewed by an allowlisted outlet resolves to **one
  `political_claims` row** whichever path (Google API or DataCommons) ingested it.
- **Stored fields / legal posture:** only claim text + categorical verdict + review
  URL + outlet + date — **never article body text**. Reading the aggregated feed
  rather than crawling an outlet's own site keeps ingestion clear of the EU sui
  generis database right.
- **Licence:** the feed **compilation is CC BY**; each ClaimReview markup carries
  its publisher's own licence in its `sdLicense` field where present. Storing only
  claim/verdict/URL/outlet/date (categorical facts, not the reviews' prose) stays
  within those terms.
- **One-shot historical backfill:** the daily feed is the steady-state path; the
  DataCommons **historical dump** is a documented one-shot. Set
  `DATACOMMONS_FEED_FORMAT=ndjson` and point `DATACOMMONS_FEED_URL` at the dump
  (one ClaimReview object per line); **gzip is auto-detected** from a `.gz` URL or
  a gzip content header, so the compressed dump decodes transparently. The same
  outlet allowlist, rating normalisation, and review-URL dedup apply.

---

## 9. Google Fact Check Tools API — broadened French corpus (`factcheck`)

**What it answers:** the same curated fast-path as section 7, from the API side.
The `claims:search` API is live (the 2025 retirement was the Search *display*, not
the API). Producer: `internal/factcheckarchive` via `cmd/factcheckcrawl`; worker and
queue are shared with the other claim paths (`factcheckworker` / `factcheck.claims`).

- **Broadened French strategy (was a fixed ~19 topics):** the run now walks two
  kinds of stream, all `languageCode=fr`:
  1. a **systematic topic rotation** over the French political domains
     (institutions, parties/figures, and the recurring policy areas — a curated
     default set overridable with `FACTCHECK_QUERIES`), and
  2. **publisher-scoped streams** (`reviewPublisherSiteFilter`, no query term) that
     page each allowlisted outlet's **entire French catalogue**, not just the
     topic-matched subset.
- **Empirical coverage (probe, `internal/factcheckarchive/probe_test.go`, live API,
  8 pages/stream sample):** the fixed 19-topic set yielded **998** unique French
  claims; the broadened strategy yielded **2 120** (about 2.1x). The probe is
  build-tagged `probe` so it never runs in CI.
- **Checkpointed and resumable:** each stream is a checkpoint unit
  (`FACTCHECK_CHECKPOINT_PATH`, a `/state` volume). A killed run resumes at the next
  undrained stream; the checkpoint clears on a fully successful run so the next
  scheduled run starts fresh.
- **Key handling:** `FACTCHECK_API_KEY` stays a **Secrets Manager entry read on the
  host** (declared in the `factcheck` connector descriptor's `Secrets`, ARN already
  in `stack/terraform/dev/main.tf`); it is never operator-forwarded.
- **Licence:** each record is schema.org ClaimReview markup surfaced by Google's
  API from the publishers; we store only claim/verdict/URL/outlet/date.

---

## 10. ClaimReview JSON-LD outlet reader (`claimreview`)

**What it answers:** the same curated fast-path for reviews the API and feed miss,
read directly from the outlets' own pages. Producer: `internal/claimreviewsite` via
`cmd/claimreviewcrawl`.

- **Allowlist-driven (EFCSN/IFCN-derived, config-curated):** only vetted French
  outlets are visited — AFP Factuel, Les Décodeurs (Le Monde), franceinfo Vrai ou
  Fake, 20 Minutes Fake Off (`config.defaultClaimReviewOutlets`).
- **Discovery is sitemap-based, never link-spidering:** each outlet's sitemap (or
  sitemap index) is fetched and page URLs are collected up to a per-outlet cap
  (`CLAIMREVIEW_MAX_URLS`).
- **robots.txt honoured, per-outlet paced:** `robots.txt` `Disallow` rules are
  enforced and its `Crawl-delay` raises the pacing floor (`CLAIMREVIEW_MIN_DELAY_MS`);
  requests carry the bot `CLAIMREVIEW_USER_AGENT`.
- **Extracts ONLY the ClaimReview fields — never the article body:** the page's
  `application/ld+json` blocks are parsed (standalone, array, or `@graph`) and only
  `claimReviewed` + rating + `url` + `datePublished` + outlet are kept.
- **Licence:** a record's `sdLicense` is honoured — a record under a reuse-forbidding
  licence (e.g. `by-nd`) is skipped. Storing only categorical facts (not the review
  prose) and preferring the official feeds/API keeps per-outlet reads conservative,
  as the EU sui generis database right requires.

---

## 11. ClaimsKG one-time seed (`claimskg`)

**What it answers:** a large historical backfill of internationally fact-checked
claims. ClaimsKG is a **2023-vintage** knowledge graph (many fact-checkers); the
operator exports it as CSV/TSV. Producer: `internal/claimskg` via `cmd/claimskgseed`.

- **Explicitly gated one-shot:** it is **host-only** (never on the scheduler) and a
  no-op unless `CLAIMSKG_SEED_ENABLED=true` **and** `CLAIMSKG_SEED_FILE` points at an
  export, so the large stale snapshot is only ingested on a deliberate operator run.
- **Provenance + vintage marked:** each record's `source_name` carries "ClaimsKG",
  the `CLAIMSKG_SEED_VINTAGE` (default 2023), and the originating fact-checker, so a
  borrowed verdict is attributable and its age visible.
- **Licence:** claim/verdict/URL/outlet/date only; the seed is a considered,
  documented import of a third-party research corpus.

---

## Shared mechanics across the claim-corpus paths (8-11)

- **One reviewable rating table:** all four paths fold an outlet's heterogeneous
  rating through `internal/claimrating` (`ratings.json`, **data not code**): textual
  phrase first (accent/case-folded, longest-match-first so "plutôt faux" beats
  "faux"), then the numeric scale, and **unmappable becomes `unverifiable`, never
  guessed**.
- **Dedup is by review URL** across every path: the claim ID is the review URL and
  the worker's upsert is keyed on it, so a claim the API, the feed, an outlet, and
  ClaimsKG all carry collapses to **one `political_claims` row**.
- **Same channel as the original `factcheck` source:** every path publishes
  `factcheckjob.ClaimJob` bodies to `factcheck.claims`, drained by the existing
  `factcheckworker`, and each is operable on the EC2 ingestion hosts via
  `scripts/ingest-host.sh crawler|consumer <source>` through its connector registry
  entry (no per-source Terraform; only `factcheck` carries a secret).

---

## 8. Institutional evidence sources (vie-publique, HATVP, Legifrance)

**What it answers:** the deeper institutional layer - *"the minister said X on date
Y"* (vie-publique public-speech metadata), *"official Z declared interest W"*
(HATVP declarations of interests), and *"the law actually says ..."* (Legifrance
consolidated code articles). All three render attributed French passages, publish
the generic `connector.EvidenceJob` to `evidence.chunks`, and are drained by
`cmd/evidenceworker` (`internal/evidencejob`) into `evidence_chunks` - no new
worker. The shared producer scaffolding (conditional-GET marker, per-identifier
manifest diff, chunk rendering, and a generic bulk-dump producer) lives in
`internal/source/evidencesrc`.

| Source id | Package / producer | Real feed | Auth | Provenance |
|-----------|--------------------|-----------|------|------------|
| `viepublique` | `internal/source/viepublique` + `cmd/viepubliquecrawl` | `echanges.dila.gouv.fr/OPENDATA/DISCOURS_PUBLICS/vp_discours.json` (JSON array, ~240 MB, ETag) | keyless | vie-publique.fr speech URL |
| `hatvp` | `internal/source/hatvp` + `cmd/hatvpcrawl` | `hatvp.fr/livraison/opendata/liste.csv` (CSV index, ETag) + `hatvp.fr/livraison/dossiers/<file>.xml` (per-declaration XML) | keyless | HATVP nominative page (`url_dossier`) |
| `legifrance` | `internal/source/legifrance` + `cmd/legifrancecrawl` | Legifrance API via PISTE `POST /consult/getArticle` (base `api.piste.gouv.fr/dila/legifrance/lf-engine-app`) | **PISTE OAuth2** | `legifrance.gouv.fr/codes/article_lc/<id>` |

**vie-publique discours.** The DILA dump is speech *metadata* only (title,
speaker(s), date, emitter, document type, themes, descriptors, URL - there is no
full-text field), so each record renders into one compact attributed passage; the
vie-publique URL opens the full speech. A conditional GET skips an unchanged dump;
the manifest diff republishes only records whose fingerprint moved. Parser written
against a real excerpt (`testdata/vp_discours.json`).

**HATVP declarations.** The CSV index is diffed by row; each new or changed
*delivered* row fetches its (small) per-declaration XML and renders a structured
summary of the declared mandates, professional activities, corporate roles and
financial holdings - with the HATVP-withheld sentinel `[Donnees non publiees]`
dropped and each empty section rendered as an explicit `neant` (a checkable fact).
A declaration whose XML fails to fetch is skipped without recording its
fingerprint, so it retries next run. Parsers written against real captures
(`testdata/liste.csv`, `testdata/lahmar-*.xml`).

**Legifrance PISTE.** Starts narrow: a configured corpus of code articles
(`LEGIFRANCE_ARTICLES`, comma-separated `LEGIARTI...=Label`) relevant to recurring
political claims (immigration, labour, security). The producer authenticates with
PISTE OAuth2 client-credentials, fetches each article via `getArticle`, diffs by
consolidated-text fingerprint, and quota-paces its requests
(`LEGIFRANCE_MIN_INTERVAL_MS`, default 500 ms). **Credentials
(`LEGIFRANCE_CLIENT_ID` / `LEGIFRANCE_CLIENT_SECRET`) come from env/secrets only**
(Secrets Manager on the host: `app/legifrance-client-id`,
`app/legifrance-client-secret`); when they are absent the run **degrades to a clean
skip** (a finished, error-free run publishing nothing), so the source can be wired
and enabled before it is provisioned. Because the endpoint is OAuth2-gated, a live
sample cannot be captured without operator credentials: `testdata/get_article.json`
matches the documented PISTE Swagger / community-published response shape, and a
real-capture validation against the live API is the operator's activation step. The
recommended starter articles are configured at activation (the operator holds the
credentials to verify each `LEGIARTI` id resolves).

**Licences and terms.**
- vie-publique discours: **Etalab Licence Ouverte / Open Licence v2.0**. Attribution:
  *"Source : DILA - vie-publique.fr, Licence Ouverte v2.0"*.
- HATVP: open data published under the HATVP reuse conditions
  (`hatvp.fr/open-data/`); declarations are public by law (art. LO 135-2 / loi
  2013-907). Attribution: *"Source : Haute Autorite pour la transparence de la vie
  publique - hatvp.fr"*. Fields the HATVP withholds are served as
  `[Donnees non publiees]` and never ingested.
- Legifrance: **Licence Ouverte v2.0** for the legal data; access is via the free
  PISTE portal (OAuth2 registration). Attribution: *"Source : DILA - Legifrance"*.

**Cloud parity.** All three run through the framework registry (manifest-driven
host script): `ENVIRONMENT=dev scripts/ingest-host.sh crawler <source> up|status|down`.
`viepublique` (08:00) and `hatvp` (08:30) run on the always-on local scheduler;
`legifrance` is **host-only / on-demand** (credential-gated, slow-moving law
corpus). State lives on per-source volumes (`viepublique-state`, `hatvp-state`,
`legifrance-state`). For `legifrance`, the operator activation step is: push the two
secrets (`scripts/push-secrets.sh`) and grant their ARNs in the crawler host's
`secret_arns` (`stack/terraform/dev/main.tf`) - the one per-source infra edit for a
secret-bearing source; the keyless two need none.

### Cour des comptes - verified, currently blocked (not shipped)

The fourth family on the card (Cour des comptes / CRC audit findings) was
**investigated and is deferred**, because its findings are only available in a form
this project cannot cleanly ingest under the card's own "no parallel PDF stack"
acceptance criterion:

- The Cour des comptes `data.gouv.fr` organisation (`cour-des-comptes`, 237
  datasets) publishes **CSV statistical annexes** to reports (effectifs, budgets,
  finances locales), **not** the report prose/findings. There is no textual dataset
  of the audit conclusions.
- The report findings ("l'audit a trouve ...") live in **PDFs** on `ccomptes.fr`,
  a Drupal site with **no JSON API, no RSS feed, and no synthesis dataset**
  (checked: `ccomptes.fr/jsonapi` -> 404, `/rss.xml` and `/fr/rss.xml` -> 404,
  `/sitemap.xml` -> URL list only; `data.ccomptes.fr` does not resolve).
- The backend has **no PDF text-extraction path**: the existing document-extraction
  path is browser-side (`react-pdf` in the frontend, POSTing extracted sentences),
  which a headless crawler cannot drive. Adding a backend PDF library is exactly the
  "parallel PDF stack" the card's AC forbids.

**Ready-path.** Cour des comptes becomes ingestible the moment either (a) a
backend/service text-extraction path exists (a Cour des comptes connector then
downloads the report PDFs, extracts text through it, and chunks with report/page
provenance into `evidence_chunks` - reusing `internal/source/evidencesrc`), or (b)
the Cour des comptes publishes an HTML/text synthesis dataset or API (then rendered
as compact passages exactly like vie-publique). The connector is a drop-in
`internal/source/courdescomptes` + one registry entry when that path lands.

---

## What the user sees today vs. "show the source" / debug mode

### What's already in the result payload

Each per-claim result frame (`claimResultFrame` in `handler/live.go`, mirrored by
`ClaimResultFrame` in `frontend/src/lib/live/frames.ts`) already carries:

| Field | Meaning |
|-------|---------|
| `verdict` / `literal` / `flags` | the judgment |
| `confidence`, `basis` (`evidence` vs `knowledge`), `rationale` | how sure / why |
| `source` | **origin of the verdict**: `curated` (borrowed from DB) vs `verified` (LLM reasoned). NOTE: this is *not* the evidence provider. |
| `matches[]` | the cited evidence, each with an `evidence_id`, plus Wikipedia `article` (title+URL) or curated `sources` |

So the raw provenance is **already on the wire** — the evidence provider is
encoded in the `evidence_id` prefix:

```
voting:...      -> Assemblee nationale / Senat
insee:...       -> INSEE
eurostat:...    -> Eurostat
ecb:...         -> Banque centrale européenne (BCE)
oecd:...        -> OCDE
wiki:...        -> Wikipedia (+ article URL)
attribution:... -> press outlet
websearch:...   -> web
```

### The gap (this is the feature card)

There is **no clean, human-readable "Source: INSEE" label** today, and there's no
single debug toggle for fact-check results. To get what you described:

- **Normal mode — just the source.** Add a derived, human-readable source label
  (e.g. *"Assemblee nationale"*, *"INSEE"*, *"Wikipedia"*) computed from the
  `evidence_id` kind of the winning citation, and render it on the verdict chip.
  Mostly a frontend mapping + a small backend helper; the data is already there.
- **Debug mode — where the info comes from, in detail.** Surface the full
  evidence list per claim: each passage's text, `evidence_id`, source URL, and
  similarity/contribution score. There's a precedent for the pattern: the
  existing `DEBUG_WIKI_SEARCH` env flag exposes a `/api/debug/wiki-search` probe
  (`handler/debug_search.go`) with a frontend hook. A
  `DEBUG_FACT_CHECK` flag could do the equivalent for live results — either a
  verbose frame variant or a UI panel that expands the already-present
  `matches[]`.

**Effort:** the plumbing exists end-to-end (`evidence_id` round-trips to the
frontend). This is largely a labelling + UI-rendering feature, not new
infrastructure — worth one card (frontend source-label mapping + an optional
backend debug flag for the detailed view).
