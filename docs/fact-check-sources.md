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

## 5. Google fact-check — does not exist

There is **no Google Fact Check Tools API, no ClaimReview, no external
fact-check archive** in the codebase. The closest things named "Google":

- **Gemini as LLM provider** (see section 4) — reads evidence, isn't a source.
- **Web search**, which is **Brave Search**, not Google
  (`internal/source/websearch/websearch.go`). It's the fallback for causal /
  comparative / untyped claims and when a preferred source returns too little.
- **Press/Attribution search**, also Brave
  (`internal/source/press/press.go`) — used to verify quotes ("X said Y").

If a Google Fact Check integration is wanted, it would be a new
`internal/source/...` adapter — it is not built today.

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
