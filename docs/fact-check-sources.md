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
