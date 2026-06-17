# Political fact-checking redesign — umbrella design

Date: 2026-06-17
Status: draft (brainstorming), pending user spec review
Scope: the anchor document for repurposing truth-in-stream as a **real-time fact-checker for
French/EU political debates and meetings**. It records the load-bearing decisions and decomposes
the work into four sub-specs, each of which gets its own design -> plan -> build cycle. It is an
architecture record, not an implementation spec.

## Reframe

The product's purpose is now specific: **fact-check live political debate and meetings (French/EU
first)**. The existing pipeline shape -- diarized streaming STT -> per-speaker sentence-boundary
buffering (VER-91) -> check-worthy gate -> claim decomposition -> tiered fast/verify pools ->
per-speaker credibility (Bayesian shrinkage) -- is a generically good spine and **transfers
wholesale**. What political speech changes is not the pipeline shape but four things the current
corpus-only design does poorly or not at all:

1. **Political facts are current and specific, not encyclopedic.** "Le chomage est a 7,3 %", "vous
   avez vote contre cette loi", "il a dit X en 2019". A pre-crawled Wikipedia corpus structurally
   cannot answer these. The `corpus-only, no live web search` non-goal is the single biggest
   mismatch for politics.
2. **The defining failure mode is true-but-misleading, not false.** Cherry-picked timeframes,
   misleading denominators, stripped qualifiers, false attribution. A binary `credible/disputed`
   verdict cannot express what a political fact-checker exists to catch.
3. **Claims repeat.** Politicians reuse talking points across every appearance. Matching spoken
   claims against a database of already-checked claims is the one place automated live
   fact-checking is solved in production (Full Fact's live tool).
4. **Neutrality and source authority *are* the product.** A political fact-checker lives or dies on
   perceived impartiality. Verdicts need primary, ideally non-partisan sources and defensible
   framing -- not a Wikipedia paraphrase. In France specifically, the fact-check outlets themselves
   are accused of slant, so leaning on their verdicts inherits their reputation.

## Load-bearing decisions (from brainstorming)

1. **Evidence layer -- live retrieval + curated DB.** Claim-match first for instant hits on repeated
   talking points; live authoritative retrieval (stats APIs, voting records, fact-check archives,
   web search) for everything novel. The verifier reads evidence it actually fetched.
2. **Verdict model -- two axes.** (a) Literal proposition: `accurate | inaccurate | unverifiable`.
   (b) Orthogonal context/manipulation flags: `missing-context | cherry-picked | outdated |
   misattributed | misleading-causation`. Splitting the objective check from the interpretive one
   is the only structure that survives a partisan/bias audit, and it is the only way to express
   "accurate, but cherry-picked timeframe".
3. **Jurisdiction -- French / EU first.** Sources: Assemblee Nationale / Senat (voting records),
   INSEE / Eurostat (statistics), Les Decodeurs / AFP Factuel / franceinfo Vrai ou Faux (fact-check
   archives). The pipeline is built so jurisdiction is a pluggable source pack; France ships first.
4. **Claim routing -- classify then route.** A claim-type classifier tags each atomic claim
   (`statistic | voting-record | attribution | causal | comparative | promise | opinion`), which
   selects a source preference + query template, then funnels through **one** retrieve-then-verify
   path. Authoritative routing without forking into N pipelines; source adapters land incrementally.
5. **Neutrality -- primary-source-first, show your work.** Prefer authoritative primary sources;
   fact-check archives corroborate only. Always show the source name + URL + quoted span.
   Conservative framing ("Selon l'INSEE..."). The verifier prompt **forbids inferring intent** -- it
   judges the claim and its framing, never motive.

## STT resolution (research, 2026-06-17)

French realtime diarized streaming is **not** a risk and needs **no provider migration**.
AssemblyAI Universal-Streaming **Multilingual** (already the basis of `u3-rt-pro`) supports French
as a first-class language with realtime word-level speaker diarization. Action is config only:
`language: "fr"`, `model_region: "global"` (holds the current $0.27/hr rate past the 2026-07-01
in-region 10% increase), `speaker_labels: true`. Speechmatics is the documented fallback for
benchmarking (explicit French realtime diarization, lower entry price, free eval tier) but there is
no reason to switch. Caveats that already match our architecture: diarization finalizes at
utterance boundaries (VER-91 sentence-boundary buffering is correct for this); adversarial crosstalk
degrades diarization on every provider.

## Target architecture (end to end)

```
audio (live stream / imported video)
 -> STT: AssemblyAI u3-rt-pro, language=fr, speaker_labels   (config change only)
 -> per-speaker sentence-boundary buffering                  (existing, unchanged)
 -> GATE: check-worthy? (FRENCH prompt)                      drops opinion / rhetoric / procedural
 -> DECOMPOSE -> 1..N atomic, coref-resolved claims (FRENCH)
 -> CLASSIFY claim type: statistic | voting-record | attribution | causal | comparative | promise | opinion
 -> ROUTE + RETRIEVE
      FAST  : semantic match vs curated pre-checked FR claim DB -> instant borrowed verdict
      VERIFY: by type ->
        statistic     -> INSEE / Eurostat / data.gouv (pull full series, not just the cited point)
        voting-record -> Assemblee Nationale / Senat scrutins (structured store)
        attribution   -> transcript / press archive search
        causal/compar -> multi-source + web search, retrieving full context
        promise       -> recorded as a tracked promise, not verified live
        opinion       -> filtered, not checked
 -> VERIFY (one LLM, FRENCH): reads evidence, emits two-axis verdict
        literal: accurate | inaccurate | unverifiable
        flags:   [missing-context, cherry-picked, outdated, misattributed, misleading-causation]
        + confidence, citations(evidence_id, quoted_span), rationale
      citation guard (deterministic): every quoted span must be a substring of retrieved evidence
 -> AGGREGATE per-speaker credibility (existing), now flag-aware
 -> EVENTS: subtitle -> claims(pending) -> result(checking) -> result(verdict + flags + source)
 -> UI overlay: verdict badge + flag chips + primary source w/ quoted span + speaker widget
```

### Catching true-but-misleading
The critical retrieval move: for a statistic, fetch the **surrounding series and adjacent periods**,
not just the cited number, so the verifier can *see* the cherry-pick. You cannot flag "cherry-picked
timeframe" if you only retrieved the cherry-picked timeframe. This is why some statistics are
pre-ingested (hot series) rather than fetched as a single live point.

## The ingestion inversion

Today the entire ingestion machine exists to **be** the evidence source: it turns Wikipedia into an
embedded `wiki_chunks` corpus the verifier reads at query time (the closed-corpus model). The new
design moves the evidence to live retrieval for novel claims, so ingestion's purpose inverts and
splits **per source -- retrieve live, or pre-ingest and index?**

| Source | Live at query time | Pre-ingest + index | Why |
|---|---|---|---|
| Fact-check archives (Les Decodeurs, AFP Factuel, franceinfo) | no | yes, vector claim DB | Bounded, stable; the fast-path matcher. Embed claim + stored verdict + primary source |
| Voting records (AN / Senat scrutins) | no | yes, **structured table** | Bulk open data; queried by deputy + bill + date, not by cosine. Deterministic, not vector |
| Statistics (INSEE / Eurostat) | yes (cached per session) | partial (hot series) | Current values move; APIs exist. Pre-ingest key series so the verifier sees the full timeline |
| Press / transcripts (attribution) | yes, search | no | Open-ended and huge; cannot pre-ingest |
| Web (causal / comparative) | yes, search | no | Open-ended |

Consequences:
- **Retired as evidence:** the Wikipedia dump path *and* the frwiki category-crawl. `frwiki` leads
  are not the right evidence for political statistics. A thin general-knowledge fallback may remain,
  but it is no longer the spine.
- **New producer:** a fact-check-archive crawler -> the curated claim DB. Reuses the existing
  crawl-path shape almost 1:1 (DB-free producer -> RabbitMQ -> competing embed fleet -> pgvector
  upsert); only the source and the extracted schema change.
- **New store type:** voting records want a structured relational table (new migration + sqlc), the
  first ingestion output that is not embedded prose.
- **New capability:** live retrieval adapters hit authoritative sources at *query time*. Nothing in
  the verify path makes an outbound call today; everything is pre-embedded. This is net-new.
- **Reusable substrate (not thrown away):** producer -> RabbitMQ -> competing-consumer fleet ->
  pgvector pattern, embed batching, vector-consistency guarantees, versioned queues, the
  worker-lifecycle controller, Fargate cloud wiring, the bastion drain.

## Decomposition -- four sub-specs

Each is a separate design -> plan -> build cycle. Build order chosen so each unblocks the next.

1. **STT / French adaptation (small).** Config: `language=fr`, `model_region=global`,
   `speaker_labels`. Plus the cross-cutting French-prompt work for the gate, decomposition,
   classifier, and verifier (French is not an STT-only concern -- it threads through every LLM
   stage). Smallest spec; mostly de-risked by the research above.
2. **Ingestion rework.** Retire the wiki dump + frwiki crawl as evidence; build the
   fact-check-archive crawler -> curated claim DB; add the voting-record structured store; add
   stat-series ingest/cache. The evidence foundation everything else verifies against.
3. **Verification core.** Claim-type classifier; routing + live source adapters behind a
   jurisdiction-agnostic `SourcePack` interface; context-aware retrieval; the two-axis verifier
   (literal verdict + flags) with the reworked citation guard; flag-aware speaker credibility.
4. **Frontend.** Two-axis verdict badges, flag chips, primary-source display with quoted span,
   the speaker-credibility widget (lie vs misleading-framing distinction).

Cross-cutting: **French language** (spec 1 threads it through specs 2-4); the existing
`FACTCHECK_VERIFY_PATH`/`FACTCHECK_POLITICAL` flag gating so `main` stays shippable; the golden eval
reframed for French political claims, including adversarial true-but-misleading cases labelled with
expected literal verdict *and* expected flags.

## Open risks / to verify (carried into the sub-specs)

1. INSEE / Eurostat / Assemblee open-data API access, rate limits, latency, and licensing for live
   calls (spec 2/3).
2. Fact-check archive ingestion ToS for the curated DB (spec 2).
3. French-capable web-search provider for causal/attribution claims (spec 3).
4. Adversarial crosstalk degrades diarization on every provider -- a product limitation to surface,
   not a bug to fix (spec 1/4).

## Non-goals / YAGNI (v1)

- Promise/prediction **tracker UI** -- capture promises, don't build the dashboard yet.
- Fully specialized per-claim-type pipelines -- routing, not forking.
- US (or other) source pack -- the interface is ready; the pack lands later.
- Replacing the transcription, diarization, or per-speaker buffering stages -- unchanged.

## Cross-references

- Superseded direction: `2026-06-17-retrieve-then-verify-factcheck-design.md`,
  `2026-06-17-speaker-credibility-scoring-design.md` (the credibility framing and two-pool
  concurrency carry forward; the corpus-only evidence model does not).
- Ingestion as it stands today: `docs/ingestion-pipeline.md`.
