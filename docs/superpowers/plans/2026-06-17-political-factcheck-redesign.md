# Political fact-checking redesign — implementation plan (epic)

> **For agentic workers:** this is an EPIC-level decomposition, not a single-subsystem bite-sized
> plan. It spans four independent subsystems (STT/French, ingestion, verification core, frontend),
> so per the writing-plans scope check it is decomposed into cards. Each card gets its own
> bite-sized TDD plan at delivery time via the workspace `/pick` flow. Steps below are card-level:
> goal, touch-points, interfaces, dependencies, acceptance.

**Goal:** Repurpose truth-in-stream as a real-time fact-checker for French/EU political debates and
meetings: live authoritative retrieval + a curated pre-checked claim DB, two-axis verdicts (literal
accuracy + manipulation flags), French throughout, neutrality-by-construction.

**Architecture:** The existing pipeline spine is unchanged (diarized STT -> sentence-boundary
buffering -> check-worthy gate -> claim decomposition -> tiered fast/verify pools -> per-speaker
credibility). This epic changes what happens after a claim exists: classify claim type, route to
French authoritative sources (live + pre-ingested), verify against evidence the model reads, and
emit a two-axis verdict. Ingestion inverts from "be the evidence corpus" to "build the curated
claim DB + structured voting store." Everything ships behind `FACTCHECK_POLITICAL` (default off);
`main` stays shippable.

**Tech Stack:** Go stdlib backend (`internal/*` layered cmd->handler->service->store), Postgres +
pgvector (`halfvec(1024)`, Voyage `voyage-4-large`), RabbitMQ ingestion fleet, AssemblyAI
u3-rt-pro streaming STT, Anthropic Claude (Haiku-class) via `internal/llm`, Next.js 16 frontend.

## Global Constraints

- Best practice + latest stable, verified via Context7 before adding any library/pattern.
- No code without tests. Go: table-driven, `go test -race ./...` green, `gofmt`/`gofumpt`,
  `go vet`, `golangci-lint run ./...`. Frontend: Vitest, ESLint. Errors wrapped with `%w`.
- Architecture boundaries: `cmd/server` wiring only -> `internal/handler` HTTP only ->
  `internal/service` (no HTTP types) -> `internal/store`. Frontend defaults to Server Components.
- Secrets from env/config only; never hard-code, commit, or log them.
- No emojis. No comments that restate code. Only touch files the card needs.
- Everything new rides `FACTCHECK_POLITICAL` (default off) until the French golden eval (O) meets
  the bar. `FACTCHECK_VERIFY_PATH` machinery and the two-pool concurrency model are reused.
- Reference design: `docs/superpowers/specs/2026-06-17-political-factcheck-redesign-umbrella-design.md`.

## Source of truth on conventions

Cards follow the seven-section structure (Outcome, Context, Approach, Acceptance criteria,
Implementation todos, Definition of Done, Code review). Cards are sized so two parallel-runnable
cards never edit the same files; the only legitimate overlap is sequenced behind a `depends_on`.
New migrations are sequenced (never two parallel cards adding the same migration number).

## Dependency DAG (build order)

```
A  French STT config + French prompts            (no deps)
C  Political evidence schema + stores            (no deps)
F  Claim-type classifier                         (no deps)
G  SourcePack interface + stats + web adapters    (no deps)
I  Two-axis verifier rework                       (no deps)
        |
C2 Fact-check-archive crawler -> claim DB         (dep: C)
C3 AN/Senat scrutins ingest -> voting store       (dep: C)
H  Voting + press/attribution adapters            (dep: G, C3)
J  Context-aware retrieval + claim-type routing   (dep: F, G, H)
L  Integrate political verify path (capstone)     (dep: I, J)
M  Frontend: two-axis badges/flags/sources        (dep: L)
O  French political golden eval + flip flag       (dep: L)
```

Parallel first wave: A, C, F, G, I. Critical path: C -> C3 -> H -> J -> L -> O/M.

Out of scope for this epic (deliberate YAGNI, follow-up epics): cloud/AWS wiring for the new
producers (mirror VER-80 later); promise/prediction tracker UI; US/other source packs;
fully specialized per-claim-type pipelines.

---

## Card A — French STT config + French prompts for live LLM stages

**Touch-points:** `internal/transcribe` (AssemblyAI session config), `internal/checkworthy`,
`internal/claimdecomp` (French prompt templates), `internal/config`.
**Deps:** none.
**Outcome:** Live transcription runs in French with speaker labels; the gate and decomposition
prompt and reason in French. Config: `language=fr`, `model_region=global`, `speaker_labels=true`.
**Acceptance:** a French audio source produces French diarized subtitles; the gate keeps French
factual statements and drops French opinions/questions; decomposition returns French atomic claims.
**Notes:** research-confirmed no provider switch (AssemblyAI u3-rt-pro multilingual). `model_region=global`
holds the current rate past the 2026-07-01 in-region increase. Restate French as a hard requirement.

## Card C — Political evidence schema + stores

**Touch-points:** one new migration `migrations/00NN_political_evidence.up.sql`/`.down.sql`,
`queries/*.sql`, `internal/store/postgres` (store methods), sqlc regen.
**Deps:** none. (Owns the single migration so C2/C3 add no schema and can run in parallel.)
**Outcome:** two stores exist: (1) a curated pre-checked claim record (claim text + embedding +
literal verdict + flags + primary source name/url + quoted span + checked-at + outlet), reusing the
`halfvec(1024)` vector convention for semantic match; (2) a structured voting-record table
(deputy/senator id + name + scrutin id + bill title + date + position for/against/abstain + source
url), queried relationally, not by cosine.
**Acceptance:** migrations apply up/down clean; sqlc-generated store methods insert and query both;
the claim store supports ANN match, the voting store supports lookup by (person, bill, date).
**Notes:** keep vector consistency guarantees (text-form `::halfvec`, never binary COPY).

## Card C2 — Fact-check-archive crawler -> curated claim DB

**Touch-points:** new `internal/factcheckarchive` (producer, DB-free), new `cmd/factcheckcrawl`,
reuse `internal/queue` + the crawl worker pattern, writes via Card C's claim store.
**Deps:** C.
**Outcome:** a crawler ingests French fact-check archives (Les Decodeurs, AFP Factuel, franceinfo
Vrai ou Faux) into curated claim records (claim + stored verdict + primary source), embedded for
the fast-path matcher. Reuses producer -> RabbitMQ -> competing-consumer -> upsert machinery.
**Acceptance:** a run over a fixture archive yields claim records with verdict + source populated and
embeddings written; re-running is idempotent. Respect each outlet's ToS/robots; fixtures match the
real wire format.
**Notes:** verify outlet ToS before live crawling; fixtures = real response shape (per the
crawl-fixtures rule). Cloud wiring is a later follow-up.

## Card C3 — Assemblee Nationale / Senat scrutins ingest -> voting-record store

**Touch-points:** new `internal/votingrecord` (open-data client + parse), new `cmd/scrutinsingest`,
writes via Card C's voting store.
**Deps:** C.
**Outcome:** AN/Senat open-data scrutins are ingested into the structured voting store: per scrutin,
each deputy's position on a dated bill, with the source url.
**Acceptance:** ingest over an AN open-data fixture populates the voting store; lookups by
(person, bill, date) return the recorded position; re-running is idempotent.
**Notes:** verify the AN/Senat open-data endpoints, formats, rate limits, licensing. Bulk/periodic
ingest, not live-per-claim (the live adapter in H reads this store).

## Card F — Claim-type classifier (internal/claimtype)

**Touch-points:** new `internal/claimtype` (thin caller over `internal/llm`), French prompt.
**Deps:** none.
**Outcome:** one cheap forced-tool Haiku call tags an atomic claim as
`statistic | voting-record | attribution | causal | comparative | promise | opinion`. Doubles as a
second filter: `opinion`/`promise` are routed away from live verification.
**Acceptance:** table-driven French examples classify correctly; opinion/promise are separated;
model error degrades to a safe default type (e.g. `causal`/generic) so the path never stalls.

## Card G — SourcePack interface + stats (INSEE/Eurostat) + web-search adapters

**Touch-points:** new `internal/source` (the `SourcePack`/retriever interface + evidence type), new
`internal/source/stats` (INSEE/Eurostat client, optional hot-series cache), new
`internal/source/websearch` (French-capable search provider).
**Deps:** none.
**Outcome:** a jurisdiction-agnostic retriever interface returns evidence passages, each with a
stable `evidence_id` + source metadata (name, url, date). Stats adapter fetches the cited figure AND
surrounding series/adjacent periods (so the verifier can see cherry-picks). Web adapter retrieves
French results for open-ended claims.
**Acceptance:** stats adapter returns a series (not just a point) for a sample indicator over a fixture;
web adapter returns passages with source metadata; per-source timeouts + caching enforced; evidence_id
round-trips. Fixtures match real wire formats.
**Notes:** verify INSEE/Eurostat API access, rate limits, licensing, and pick the web-search provider
via Context7. Interface stays jurisdiction-neutral (future US pack drops in).

## Card H — Voting-record + press/attribution source adapters

**Touch-points:** `internal/source/voting` (reads Card C's voting store), `internal/source/press`
(transcript/press search for attribution claims). Implements Card G's interface.
**Deps:** G (interface), C3 (voting store populated).
**Outcome:** a voting adapter answers "did X vote for/against bill Y" from the structured store; a
press/attribution adapter retrieves quote evidence for "Z said ...".
**Acceptance:** voting adapter returns the recorded position as an evidence passage with the AN/Senat
source url; press adapter returns quote passages with source + date; both emit stable evidence_ids.

## Card I — Two-axis verifier rework (internal/verify)

**Touch-points:** `internal/verify` (tool contract + prompt + citation guard), French prompt.
**Deps:** none (operates on supplied evidence passages; isolated package rework).
**Outcome:** the verifier emits two axes: `literal: accurate | inaccurate | unverifiable` PLUS
`flags: [missing-context | cherry-picked | outdated | misattributed | misleading-causation]`, with
confidence, citations (evidence_id + quoted_span), and a French rationale. The prompt forbids
inferring intent. Citation guard (deterministic): every quoted span must be a substring of a
supplied passage; fabricated citations dropped; an evidence-basis verdict that loses all citations
degrades basis, not state.
**Acceptance:** adversarial French pairs: a contradicted statistic -> `inaccurate`; a true figure on
a cherry-picked timeframe (full series supplied) -> `accurate` + `cherry-picked`; a stripped quote ->
`misattributed`/`missing-context`; a subjective claim -> `unverifiable`. Citation guard unit-tested.

## Card J — Context-aware retrieval + claim-type routing (match.go)

**Touch-points:** `internal/service/match.go` (rework), wiring to `internal/claimtype` +
`internal/source` adapters.
**Deps:** F (classifier), G + H (adapters).
**Outcome:** retrieval routes by claim type to the preferred SourcePack adapter(s), pulls full
context (series/adjacent periods for stats), and falls back to web when thin. The atomic claim text
(coref-resolved) is the query. Each passage carries a stable evidence_id for citation round-trip.
**Acceptance:** a statistic claim routes to the stats adapter and returns a series; a voting claim
routes to the voting adapter; an attribution claim routes to press; thin results broaden to web;
evidence_ids round-trip into the verifier.

## Card L — Integrate political verify path behind FACTCHECK_POLITICAL (capstone)

**Touch-points:** `internal/service/live.go` (orchestration), the per-speaker credibility aggregator
(make it flag-aware: separate "misleading-framing" tally from outright `inaccurate`), event model
(`internal/service` + frame types), `internal/config` (`FACTCHECK_POLITICAL`). Demote the wiki
corpus to an optional general-knowledge fallback (no deletion).
**Deps:** I (verifier), J (routing). Folds in the flag-aware aggregator change.
**Outcome:** behind `FACTCHECK_POLITICAL`, a claim flows classify -> route+retrieve -> two-axis
verify -> flag-aware speaker score, emitting per-claim events (subtitle -> claims(pending) ->
result(checking) -> result(verdict+flags+source)). Fast path still borrows curated claim-DB matches
instantly. Reuses the two-pool concurrency + capacity-shed semantics.
**Acceptance:** with the flag on, a faked transcribe + pools assert the event lifecycle and that a
verdict carries literal + flags + source; the speaker score moves on `accurate`/`inaccurate` and the
misleading-framing tally moves on flags; capacity-shed yields an honest per-claim terminal state;
flag off = today's behaviour unchanged.

## Card M — Frontend: two-axis verdicts, flags, sources, speaker credibility

**Touch-points:** `stack/frontend` — `verdict-badge.tsx`, `lib/live/frames.ts`,
`lib/live/claims.ts`, `lib/live/summary.ts`, the source-display + speaker-credibility components.
**Deps:** L (events/frames defined).
**Outcome:** the live overlay renders the literal verdict badge + flag chips + primary source (name,
url, quoted span) per claim, and a per-speaker credibility widget distinguishing falsehood from
misleading framing. French copy.
**Acceptance:** Vitest covers badge rendering for all literal states + each flag chip, the source
affordance, frame/summary parsing of the new fields, and the speaker widget across frames.

## Card O — French political golden eval + accuracy gate; flip FACTCHECK_POLITICAL

**Touch-points:** `internal/eval` (`testdata/golden.json` reframed), the accuracy-gate test.
**Deps:** L (path wired end to end).
**Outcome:** a French political golden set (labelled across all claim types, including adversarial
true-but-misleading cases, each with expected literal verdict AND expected flags) runs as a
regression gate. Documents the real-model eval procedure and the flag-flip decision (mirror the
retrieve-then-verify eval discipline: offline faked-model gate guards wiring; flip the default only
after a real-model run meets the bar).
**Acceptance:** offline faked-model gate is green and fails on regression; the flip procedure is
documented; default stays off until the real-model bar is met.

## Self-review

- Spec coverage: every umbrella-spec element maps to a card — evidence layer (C/C2/C3/G/H/J),
  two-axis verdict (I/M), French (A + per-card prompts), routing (F/J), neutrality (I/M source
  display), ingestion inversion (C/C2/C3 + L demotes wiki), eval/flag (O). STT resolved in A.
- No placeholders: each card states files, deps, and acceptance. Migration number `00NN` is the one
  intentional late-bind (assigned at delivery against the then-current migration head).
- Type/name consistency: `SourcePack`/evidence_id (G) consumed by H/J; two-axis verdict shape (I)
  consumed by L/M/O; claim-type enum (F) consumed by J.
