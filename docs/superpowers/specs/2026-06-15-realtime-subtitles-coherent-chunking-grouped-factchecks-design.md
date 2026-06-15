# Real-time subtitles, coherent chunking, and grouped fact-checks

Date: 2026-06-15
Status: Approved (design)

## Problem

During live analysis (live streams and imported videos alike), the subtitle and
fact-check experience has three weaknesses:

1. **Subtitles do not feel real-time.** The viewer effectively sees only "the
   beginning of the section that is about to be analysed" rather than speech
   building up word-by-word.
2. **Sections are cut mid-sentence.** The chunk boundary used to decide what gets
   fact-checked can land in the middle of a sentence, so a chunk sent for
   checking is not a coherent thought.
3. **Related results are scattered.** When several statements across different
   subtitle sections are about the same fact, their fact-check results are shown
   as separate rows instead of being grouped and scored together.

The goal: subtitles that stream in real time, chunk boundaries that fall at
coherent sentence ends, and fact-check results grouped by the fact they concern,
each group carrying a single aggregated score against the enwiki dataset, shown
in the fact-checked section directly under the subtitles.

## Current behavior (baseline)

- **Transcription** (`stack/backend/internal/transcribe/assemblyai.go`): connects
  to AssemblyAI Universal streaming v3. In-progress turns are emitted as non-final
  `TranscriptEvent`s (partials); committed turns (`end_of_turn=true`) as final
  events. `max_turn_silence` is set to **500 ms** (`assemblyAIMaxTurnSilenceMs`),
  so a turn commits after half a second of silence.
- **Interim wire path** is complete and tested: the analyzer forwards non-final
  transcripts as `LiveEventInterim` (`internal/service/live.go:362-366`), the
  handler serializes them as `{type:"interim", text}` frames, and the frontend
  renders them in `LiveCaption` (`stack/frontend/src/app/app/_components/live-fact-check-panel.tsx`).
- **Chunking** (`internal/service/live.go` `analyzeLoop`): consecutive
  same-speaker committed segments accumulate into a `liveUnit`. A unit flushes on
  a speaker change, when it would exceed a 3-sentence cap (`defaultMaxSentences`),
  or after a 2 s idle timer (`defaultIdleFlush`). The idle flush can emit a buffer
  whose last committed turn ended mid-sentence.
- **Scoring** (`internal/service/match.go`, `confidence.go`): a unit's combined
  text is embedded (voyage-4-large, 1024 dims), matched against the curated claims
  corpus and the `wiki_chunks` (enwiki) corpus, and scored. `Confidence` exposes
  `Score`, `Supporting`, `Contradicting`, `EvidenceItems`. Each member segment
  receives the same matches and confidence, keyed to its own subtitle id.
- **Frontend display** (`live-statement-list.tsx`, `live-fact-check-list.tsx`):
  the subtitle region renders committed statements; the fact-check region renders
  a flat list, one row per match (`deriveFactChecks` in `src/lib/live/...`), each
  row linking back to its origin statement for cross-highlighting.

## Decisions

- **Real-time fix is at the source**: raise `max_turn_silence` so the live caption
  spans a whole coherent section instead of resetting every ~500 ms.
- **Cut boundary**: sentence-aware heuristic (no LLM). Never flush mid-sentence.
- **Grouping rule**: group results that resolve to the same matched enwiki
  claim/source.
- **Group display**: one merged card per fact — shared claim/source, a single
  aggregated score, the contributing statements listed beneath.
- **Grouping lives in the frontend** derive layer; no backend group events.

## Design

### 1. Real-time subtitles (backend transcribe)

Root cause of the "only the beginning" symptom is the 500 ms turn commit: the
interim caption barely grows before the turn finalizes.

- Raise `assemblyAIMaxTurnSilenceMs` to a value that lets a natural sentence form
  (target ~1500 ms), expressed as a named constant. AssemblyAI then keeps
  streaming partials for the current utterance longer, so `LiveCaption` builds up
  word-by-word until a genuine pause.
- No frontend change is required for the caption to grow — the interim render path
  already exists. During the e2e check, verify partials actually arrive; only if
  v3 requires it, additionally set `format_turns`. The silence threshold is the
  primary lever.

Tests: `assemblyAIURL` test asserts the query carries the new `max_turn_silence`.

### 2. Sentence-aware section cuts (backend service)

Keep speaker-change and the hard sentence/word cap as forced flushes. Change the
**idle** path so it never emits a mid-sentence buffer:

- Track whether the unit's accumulated text currently ends on a sentence boundary
  (terminal punctuation `. ! ? …`), reusing the existing `sentenceCount` scanner's
  terminal-rune logic (extract a small `endsAtSentenceBoundary(text)` helper).
- On the idle timer: if the buffer ends at a boundary, flush as today. If it does
  not, extend a short bounded grace window to let the sentence finish. A hard
  maximum (cap on total buffered sentences/words or a max-hold duration) guarantees
  the buffer can never hang, so a never-completing sentence still flushes.
- Net effect: chunks sent for checking are complete thoughts.

This is a pure logic change inside `analyzeLoop`/`liveUnit`. The worker-pool,
queue, and backpressure behavior are unchanged.

Tests: table-driven cases for `endsAtSentenceBoundary` and for the flush decision
(boundary vs. mid-sentence idle, grace-window completion, hard-cap force flush,
speaker change). `go test -race ./...`.

### 3. Group related results by matched claim/source (frontend derive)

Grouping is a display concern, so it stays in the frontend derive layer alongside
`deriveFactChecks`, keeping the backend untouched.

- Add `deriveFactCheckGroups()` that, for each **checked** statement with a
  confident match, computes a stable group key from its top-ranked match's
  identity: the curated claim id, else the matched enwiki article URL.
- Statements sharing a key are clustered into one group regardless of speaker or
  subtitle section. Statements with no confident match are not grouped (they keep
  the existing per-statement treatment / "no confident match").
- Each group records its member statement ids (for cross-highlight) and the shared
  claim/source metadata.

### 4. Grouped display with one aggregated enwiki score (frontend components)

Replace the flat one-row-per-match fact-check region with grouped cards. Each card
renders:

- the shared claim/source (enwiki article title/URL or curated claim text and its
  verdict),
- a single **aggregated score** recomputed from the members' raw `Supporting` and
  `Contradicting` weights: `score = sum(supporting) / (sum(supporting) +
  sum(contradicting))`, mirroring `computeConfidence` semantics so the group score
  reflects the whole group's corroboration against the enwiki dataset,
- the contributing statements beneath, each clickable to scroll/highlight its
  subtitle via the existing cross-highlight wiring.

The region remains directly under the subtitles in `live-fact-check-panel.tsx`.

Tests (Vitest): `deriveFactCheckGroups` grouping by claim id / article URL; score
aggregation math; the grouped component renders one card per fact and a click
selects the right statement id.

## Boundaries and standards

- Go layering respected: flush logic in `internal/service`, provider config in
  `internal/transcribe`. Errors wrapped with `%w`. gofumpt, `go vet`,
  golangci-lint clean.
- Frontend: Server Components by default, `'use client'` only where already
  present; ESLint clean. Vitest pinned to `stack/frontend` cwd.
- Every behavior change ships with its tests in the same change.

## Testing summary

- Go: table-driven flush + boundary tests, URL config test, `go test -race ./...`.
- Frontend: Vitest for grouping, score aggregation, grouped render + cross-link.
- E2E: run an imported video through the live WS and confirm (a) the caption
  streams in real time, (b) chunks end at sentence boundaries, (c) related
  statements collapse into one scored card under the subtitles.

## Out of scope (YAGNI)

- Topic/entity-based grouping (only same matched claim/source).
- LLM-assisted boundary detection (heuristic only).
- Backend-emitted grouping events (grouping is a frontend derivation).
