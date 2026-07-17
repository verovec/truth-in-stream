# Pending Linear cards - Epic D (video pre-analysis)

Staged card bodies for the `epic:preanalysis` cards. The Linear connector was not
authenticated when this epic was drafted; create these seven cards verbatim (team
Veroveit, project Truth in Stream, state `Todo`, label `epic:preanalysis`, priorities as
noted), record the assigned VER-IDs in `EPICS-TRUTH-IN-STREAM.md`, re-run `/roadmap`,
then delete this file.

Dependencies to record: D2 depends_on D1; D3 depends_on D1, D2; D4 depends_on D3;
D5 depends_on D4; D6 depends_on D3; D7 depends_on D5, D6.

The audio-extraction piece (D2) and the claim timeline (D5) are split out from the job
(D3) and the player plumbing (D4) on purpose: D2 is the epic's one genuinely novel,
unprecedented capability (no existing server-side path reads a stored video today) and
benefits from being validated in isolation before a job is built around it; D5 is the
one piece of UI with no existing pattern to mirror. Neither split exists to avoid
merge conflicts (the chain is serial either way) - it keeps each card's review surface
focused.

---

## D1 - Durable video analysis storage and read API (priority: High)

**Outcome**

The operator's fact-check results survive beyond the 24 h replay cache: a completed
video analysis is stored durably in Postgres, playback and exports read it from there,
and every video listing tells the operator whether a video has been analysed.

**Context**

Foundation card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`). Today the only reuse
of an analysis is the Redis snapshot cache keyed `analysis:v1:{video.ID}`
(`stack/backend/internal/store/analysiscache.go`,
`stack/backend/internal/service/snapshot.go`), replayed through the
`AnalysisRecorder`/`AnalysisReplayer` ports in
`stack/backend/internal/handler/live.go`. This card adds the durable Postgres layer
under those same ports. The documents feature is the schema and lifecycle precedent
(`stack/backend/migrations/0012_documents.up.sql`,
`stack/backend/internal/domain/document.go`). Later cards in the epic write through the
store added here (the pre-analysis job) and read the API added here (the frontend
cards). No job, no ffmpeg, no frontend in this card.

**Approach**

Follow the layered backend architecture: handler (HTTP only) -> service (no HTTP types)
-> store, sqlc for queries, migrations numbered after the latest in
`stack/backend/migrations` (expected `0019`; verify no concurrent branch claimed it).
Mirror the documents vocabulary: `videos` gains `analysis_status`
(`none|analysing|complete|failed`), `analysis_error`, `analyzed_at`, `analysis_runs`,
`analysis_progress_ms`. New table `video_analyses` (one row per video, upsert on
re-analysis): `video_id` PK/FK cascade, `snapshot_version`, `events jsonb` (the ordered
live events, absolute video-time timestamps), `engine jsonb` (model identifiers and
config fingerprint), denormalized claim counters
(`claims_total/credible/disputed/unverifiable`), timestamps. The replayer becomes a
composite: Postgres first, then Redis, then miss; the recorder used by live views stays
Redis-only so a lossy browser view never overwrites a deliberate pre-analysis. Wire in
`stack/backend/cmd/server/main.go`. Read API: `GET /api/videos/{id}/analysis` returns
status, error, progress, `analyzed_at`, runs, engine, counters, and (when complete)
`frames` shaped exactly like the WS serializer output so the frontend reuses its frame
reducers; `GET /api/videos` items gain `analysis_status` and `analyzed_at`. Verify
current pgx/sqlc idioms against the versions pinned in `stack/backend/go.mod` before
writing queries.

**Acceptance criteria**

* A stored analysis is served by the live WebSocket fast-path (no transcriber or LLM
  touched) and by the SRT/CSV export endpoints, permanently, not TTL-bound.
* `GET /api/videos/{id}/analysis` reports lifecycle state and, when complete, the full
  frame list with absolute timestamps.
* Every video list item carries `analysis_status` without extra queries per row.
* A Redis-cached live replay still works for videos with no stored analysis; when both
  exist, Postgres wins.

**Implementation todos**

- [ ] Migration `0019` (up/down): `videos` analysis columns + `video_analyses` table
- [ ] Queries in `stack/backend/queries/video_analyses.sql` (+ videos.sql lifecycle
      updates): upsert analysis, get analysis, claim/flip status conditionally, recover
      orphans, list flag; regenerate sqlc
- [ ] `stack/backend/internal/store/postgres/video_analyses.go` wrapper + domain types
- [ ] Composite replayer (Postgres -> Redis) behind the existing `AnalysisReplayer`
      port; recorder unchanged (Redis-only); wiring in `cmd/server/main.go`
- [ ] `GET /api/videos/{id}/analysis` handler + frame serialization shared with the WS
      writer (extract, do not duplicate)
- [ ] `analysis_status`/`analyzed_at` on list/get video payloads
- [ ] Table-driven tests: composite precedence, upsert + counters, conditional status
      transitions, handler status codes, list flag

**Definition of Done**

- [ ] Library/idiom choices verified against current docs before coding
- [ ] `go test -race ./...` green; new logic covered by table-driven tests
- [ ] `gofmt`/`gofumpt`, `go vet ./...`, `golangci-lint run ./...` clean
- [ ] Errors wrapped with `%w`; no dead code; only task files touched
- [ ] No secrets or infrastructure identifiers committed
- [ ] PR opened against `dev`, CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff; resolve every correctness finding; address or
      justify quality findings; re-review after changes. Not Done until the review
      passes.

---

## D2 - ffmpeg audio-extraction adapter and pacing (priority: High, depends_on: D1)

**Outcome**

The backend can turn any stored video into a paced stream of 16 kHz PCM frames shaped
exactly as AssemblyAI's realtime API expects, verified in isolation before any job or
endpoint is built around it.

**Context**

Second card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`), split out from the
pre-analysis job card on purpose: this is the epic's one genuinely novel, unprecedented
capability - no path in this codebase reads a stored video object server-side today
(imported-video analysis is entirely browser-driven, see
`stack/frontend/src/lib/live/audio-capture.ts`). Isolating it lets the risky,
needs-real-world-validation piece get built and tested on its own, ahead of the
job-lifecycle wrapper (next card) that closely mirrors the proven `DocumentAnalyzer`
pattern. ffmpeg is already installed in both backend images (yt-dlp dependency,
`stack/backend/Dockerfile`). No job, no lock, no HTTP endpoint in this card - the
output is a plain reader/channel of paced PCM frames that a later job feeds into
`LiveAnalyzer.Run`.

**Approach**

New `stack/backend/internal/audioextract` package, exec pattern mirrored from
`internal/ytdlp`: given a video's stored object (opened via `domain.MediaStore`), run
ffmpeg to decode to 16 kHz s16le mono PCM on stdout. A chunker slices that stream to
~100 ms frames - AssemblyAI closes with 3007 on frames outside 50-1000 ms (see the live
path's existing pacing comment in `stack/backend/internal/transcribe`). A pacer submits
frames at realtime by default, configurable via `PREANALYSIS_PACING_FACTOR`; verify
AssemblyAI streaming's documented tolerance for faster-than-realtime submission via
Context7/current docs before enabling any factor above 1.0 - correctness over speed.
Design the package's public surface (e.g. an `Extract(ctx, source) (<-chan []byte,
error)` or an `io.Reader` wrapper) so the next card's job service can consume it without
knowing about ffmpeg, exec, or chunking internals.

**Acceptance criteria**

* Given a stored video object, the adapter produces a stream of PCM frames each within
  AssemblyAI's 50-1000 ms bound, paced at the configured factor.
* ffmpeg failures (bad/corrupt media, missing binary) surface as a typed error, not a
  panic or a hang.
* The component has no dependency on video/job/HTTP types - it is usable standalone in
  a later job or a CLI debugging tool.

**Implementation todos**

- [ ] `internal/audioextract`: ffmpeg exec adapter -> 16 kHz s16le mono PCM stream
- [ ] Chunker: slice to ~100 ms frames, enforce the 50-1000 ms bound
- [ ] Pacer: realtime default, `PREANALYSIS_PACING_FACTOR` config, documented
- [ ] Fake-exec unit tests: happy path, ffmpeg failure, malformed output, frame-size
      bounds, pacing timing (via an injectable clock, not real sleeps)
- [ ] Verify AssemblyAI streaming's faster-than-realtime tolerance against current docs
      before defaulting the pacing factor away from 1.0; record the finding in the PR

**Definition of Done**

- [ ] AssemblyAI streaming pacing guidance verified against current docs and recorded
      in the PR description
- [ ] `go test -race ./...` green; new logic covered by table-driven tests
- [ ] `gofmt`/`gofumpt`, `go vet ./...`, `golangci-lint run ./...` clean
- [ ] Errors wrapped with `%w`; no dead code; only task files touched
- [ ] No secrets or infrastructure identifiers committed
- [ ] PR opened against `dev`, CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff; resolve every correctness finding; address or
      justify quality findings; re-review after changes. Not Done until the review
      passes.

---

## D3 - Headless server-side pre-analysis job for imported videos (priority: High, depends_on: D1, D2)

**Outcome**

The operator triggers one server-side analysis of a ready imported video and can re-run
it later when the evidence corpus has changed. The job transcribes and fact-checks the
stored media without any browser involved, persists the result durably, reports
progress, and survives restarts. Once a video is analysed, opening it never spends
transcription or LLM budget again.

**Context**

Third card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); requires the storage
and read API card (D1) and the audio-extraction adapter (D2). Today imported-video
analysis is browser-driven per view: the client streams playback audio to
`GET /api/videos/{id}/live` -> `service.LiveAnalyzer` -> AssemblyAI streaming v3 ->
verify path. `DocumentAnalyzer`
(`stack/backend/internal/service/document_analyzer.go`) is the job-lifecycle
precedent: conditional-update lock, injectable `spawn`, detached timeout-bounded
context, terminal writes on a short separate context, startup recovery. This card is
the lifecycle wrapper around D2's audio stream - it should not need to know about
ffmpeg or chunking, only consume D2's public interface. Live TV and non-analysed
imported videos keep the live pipeline untouched.

**Approach**

`VideoAnalyzer` service mirrors `DocumentAnalyzer`: `Start` requires video status
`ready`, claims the `analysis_status='analysing'` lock (409 on conflict), spawns the
run; the run opens D2's audio stream for the video and feeds it to `LiveAnalyzer.Run`
exactly as the live handler does, tees emitted events, updates
`analysis_progress_ms` periodically, and on flush upserts `video_analyses` (events,
engine metadata, counters) then flips `complete`; failures flip `failed` with the
error. `Recover()` marks orphaned `analysing` rows `failed` at startup. Global
concurrency semaphore `PREANALYSIS_MAX_CONCURRENT` (default 1). Route
`POST /api/videos/{id}/analyse` behind `RequireAdmin` (202 accepted, 409 analysing, 422
not ready); the same endpoint re-runs from `complete`/`failed`, and the previous
analysis stays readable until the new run completes. Config in
`stack/backend/internal/config` with documented env vars, forwarded in
`docker-compose.yml` (compose does not pass env vars through implicitly).

**Acceptance criteria**

* Clicking analyse on a ready video produces, without any browser open, a stored
  analysis identical in shape to a live session's (subtitles, claims, verdicts,
  speaker data), and the video then plays back from the stored analysis.
* Progress is visible while running; a backend restart mid-run leaves the video
  `failed` and re-runnable, never stuck `analysing`.
* Re-analysing a completed video overwrites its stored result and bumps
  `analysis_runs`; concurrent start attempts get 409.
* Live TV streams and non-analysed imported videos behave exactly as before.

**Implementation todos**

- [ ] `VideoAnalyzer` service: lock claim, spawn, run (consuming D2's audioextract
      output), progress, terminal writes, `Recover()` wired at startup in
      `cmd/server/main.go`, concurrency semaphore
- [ ] Engine metadata + claim counters computed from the teed events at completion
- [ ] `POST /api/videos/{id}/analyse` handler (RequireAdmin) + route registration
- [ ] Config: `PREANALYSIS_MAX_CONCURRENT`, run timeout; forwarded in
      `docker-compose.yml`; documented in the config reference
- [ ] Verify SRT/CSV exports and the WS fast-path serve the job's stored analysis
- [ ] Table-driven tests: lifecycle transitions, recovery, 409/422 paths

**Definition of Done**

- [ ] `go test -race ./...` green; new logic covered by table-driven tests
- [ ] `gofmt`/`gofumpt`, `go vet ./...`, `golangci-lint run ./...` clean
- [ ] Errors wrapped with `%w`; no dead code; only task files touched
- [ ] No secrets or infrastructure identifiers committed
- [ ] PR opened against `dev`, CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff; resolve every correctness finding; address or
      justify quality findings; re-review after changes. Not Done until the review
      passes.

---

## D4 - Analysed playback plumbing: stored subtitles and pre-analyse control (priority: High, depends_on: D3)

**Outcome**

Opening a pre-analysed video feels instant and complete: the full transcript is there
before playback starts and the active line highlights as the video plays, claims and
verdicts render from the stored result, and no transcription or fact-checking runs
during playback. The operator can trigger a pre-analysis right from the player, and
library tiles show which videos are already analysed.

**Context**

Fourth card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); consumes the read API
(D1) and the job trigger (D3). The watch screen is
`stack/frontend/src/app/app/_components/library-experience.tsx` hosting
`PlaybackProvider`, `LiveAnalysisProvider`, `VideoPlayer` (react-player, native
controls) and the live panels. `use-live-analysis.ts` currently always opens the WS on
play and shifts frame timestamps by the playback position at socket open
(`prepareFrame`/`baseTime`) - stored frames carry absolute video time, so hydrating
over the WS would corrupt timestamps on a mid-video open; that is why analysed videos
hydrate over REST and never open the socket. `LiveStatementList` already highlights the
active segment from `currentTime` (binary search in `lib/fact-check/segments.ts`). The
claim timeline strip is a separate, later card (D5) - this card is the data plumbing
and controls only, no new timeline visualization. Backoffice UI is a separate card
(D6); do not touch `stack/frontend/src/app/backoffice/*`.

**Approach**

Respect Server Components by default; `'use client'` only at the leaves that need it.
On selecting a video with `analysis_status === 'complete'` (from the list payload),
fetch `GET /api/videos/{id}/analysis` and hydrate the existing live-analysis store by
running the returned frames through the existing reducers in `lib/live/*` with base
time 0; skip `useLiveAnalysis` socket + `createMediaElementCapture` entirely for these
videos. Non-analysed videos keep the live flow byte-for-byte. Admin-only pre-analyse
button (status from the identity already exposed to the app shell) when status is
`none`/`failed`, calling `POST /api/videos/{id}/analyse`; while `analysing`, poll the
analysis endpoint every 2 s and show progress; non-admins see a status chip only.
Library tiles get an "Analysed" badge from the list payload. Match the existing app
copy conventions for all new strings.

**Acceptance criteria**

* A pre-analysed video shows its full transcript immediately; the active subtitle
  highlights in sync with playback, including after seeking; no WebSocket opens and no
  audio capture starts.
* An admin sees and can use the pre-analyse button on a ready, un-analysed video and a
  live progress indicator while it runs; a non-admin cannot trigger analysis.
* Videos without a stored analysis keep the current live-analysis experience
  unchanged.
* Library tiles show an "Analysed" badge from the list payload.

**Implementation todos**

- [ ] Analysis fetch + store hydration path (frames -> existing reducers, base time 0)
- [ ] Gate in the player wiring: analysed -> REST hydration; otherwise live WS
      (unchanged)
- [ ] Pre-analyse button + polling progress chip; admin gating; failed-state retry
- [ ] "Analysed" badge on library tiles from `analysis_status`
- [ ] Vitest: hydration equivalence with a WS session fixture, no-socket/no-capture
      assertion for analysed videos, button state machine, badge rendering

**Definition of Done**

- [ ] Component/idiom choices verified against the pinned Next.js/React versions
- [ ] Vitest suite green; new behaviour covered
- [ ] ESLint clean; TypeScript strict passes
- [ ] Only task files touched; no secrets committed
- [ ] PR opened against `dev`, CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff; resolve every correctness finding; address or
      justify quality findings; re-review after changes. Not Done until the review
      passes.

---

## D5 - Claim timeline strip: verdict-colored playback overview (priority: Medium, depends_on: D4)

**Outcome**

A pre-analysed video shows, at a glance, where in the timeline claims were checked and
how they resolved: a strip under the player marks each checked claim by time and color
(credible, disputed, unverifiable), and clicking a marker jumps playback there.

**Context**

Fifth card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); depends on D4, which
hydrates the live-analysis store this card reads from. Split out from D4 deliberately:
this is the one piece of UI in the whole epic with no existing pattern to mirror - the
player uses native controls and there is no custom seek bar anywhere in the frontend
today - so it deserves its own focused review and iteration room rather than riding
along with the more mechanical hydration/button work. Verdict colors already exist in
`live-claim-verdict.tsx`; reuse that palette rather than inventing a new one.

**Approach**

A client-leaf component rendered under the player, aligned to `duration` (do not
rebuild the native scrubber - render alongside it). One marker or segment per checked
claim, positioned by its parent statement's `[start, end]` span, colored by verdict:
credible and disputed prominent, unverifiable muted. Hover reveals the claim text and
verdict; click seeks through the playback store. Render only when the video is
pre-analysed (i.e. only alongside the hydrated store from D4); render nothing when the
claim list is empty rather than an empty strip. Handle dense marker clusters
gracefully (this is the epic's "hot moments" signal - visible marker density, not a
computed heat score) - overlapping markers should remain individually hoverable/
clickable, not silently collapse.

**Acceptance criteria**

* The timeline strip marks every checked claim at its time span, colored by verdict.
* Clicking a marker seeks the player to that time; hovering identifies the claim text
  and verdict.
* Dense clusters of markers remain individually interactive.
* The strip does not render for videos without a stored analysis.

**Implementation todos**

- [ ] Timeline strip component: positioning math from claim spans + `duration`
- [ ] Verdict coloring via the existing palette (`live-claim-verdict.tsx`)
- [ ] Hover (claim text + verdict) and click-to-seek (playback store)
- [ ] Dense-cluster handling (overlap layout or stacking, still individually
      interactive)
- [ ] Vitest: positioning math, verdict coloring, click-to-seek, empty-claims
      no-render, dense-cluster interactivity

**Definition of Done**

- [ ] Component/idiom choices verified against the pinned Next.js/React versions
- [ ] Vitest suite green; new behaviour covered
- [ ] ESLint clean; TypeScript strict passes
- [ ] Only task files touched; no secrets committed
- [ ] PR opened against `dev`, CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff; resolve every correctness finding; address or
      justify quality findings; re-review after changes. Not Done until the review
      passes.

---

## D6 - Backoffice: video analysis status and analyse/re-analyse controls (priority: High, depends_on: D3)

**Outcome**

From the backoffice the operator sees at a glance which videos are analysed, when, and
with what result volume, and can analyse a new video or re-analyse an old one after the
evidence corpus has changed - with a confirmation step, since re-analysing overwrites
the stored result.

**Context**

Backoffice card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); consumes the read API
(D1) and trigger endpoint (D3). File-disjoint from D4/D5 (the player cards) - runs in
parallel with them, both depending only on D3. The videos section lives in
`stack/frontend/src/app/backoffice/_components/backoffice-videos-section.tsx` +
`backoffice-video-list.tsx` (kind/status badges, two-step delete, 2.5 s polling for
pending YouTube ingests). The documents reanalyse control
(`stack/frontend/src/app/documents/_components/reanalyse-control.tsx`) is the trigger
precedent (409/conflict handling). This card touches only
`stack/frontend/src/app/backoffice/*` and the shared video API client.

**Approach**

Extend `backoffice-video-list.tsx` rows with an analysis column: `none` (quiet),
`analysing` with live % (reuse the section's polling rhythm while any row is
analysing), `complete` with `analyzed_at` date and claim counters, `failed` with the
error surfaced. Actions per row: "Analyse" for ready+un-analysed, "Re-analyse" behind
a two-step confirm (same interaction pattern as the existing two-step delete) for
completed ones, both calling `POST /api/videos/{id}/analyse` via the shared client in
`stack/frontend/src/lib/video/api.ts`; handle 409 (already running) and 422 (not
ready) with inline feedback. Keep Server Components by default, client leaves only
where interaction requires it; match existing backoffice copy conventions.

**Acceptance criteria**

* The backoffice video list shows analysis state, date, and claim counts for every
  video without opening it.
* Analyse and re-analyse work from the list; re-analyse asks for confirmation;
  progress appears while a job runs and resolves to the new result.
* Conflict (already analysing) and not-ready videos get clear inline feedback, not
  silent failures.

**Implementation todos**

- [ ] Extend the video API client with analyse + analysis-status calls
- [ ] Analysis column/badges + counters in `backoffice-video-list.tsx`
- [ ] Analyse / two-step re-analyse actions with 409/422 handling
- [ ] Polling while any row is `analysing`, stopping when idle
- [ ] Vitest: badge states, confirm flow, conflict feedback, polling start/stop

**Definition of Done**

- [ ] Vitest suite green; new behaviour covered
- [ ] ESLint clean; TypeScript strict passes
- [ ] Only task files touched; no secrets committed
- [ ] PR opened against `dev`, CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff; resolve every correctness finding; address or
      justify quality findings; re-review after changes. Not Done until the review
      passes.

---

## D7 - Pre-analysis docs and end-to-end verification close-out (priority: Medium, depends_on: D5, D6)

**Outcome**

The feature is documented and proven end to end: an operator following the docs can
ingest a video, pre-analyse it, watch it with stored subtitles and the verdict
timeline, re-analyse from the backoffice, and export the results - and the docs
explain when live analysis still applies.

**Context**

Close-out card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); starts once the
timeline card (D5) and the backoffice card (D6) are merged or in review.
Documentation lives under `docs/` (architecture, config reference, local setup); the
config reference gained an ingestion refresh recently - extend, do not fork, its
structure.

**Acceptance criteria**

* Docs cover the pre-analysis lifecycle (trigger, progress, re-analyse, failure
  recovery), the new env vars, and the live-vs-preanalysed decision path.
* A scripted end-to-end pass on the local stack exercises ingest -> analyse ->
  analysed playback (subtitles + timeline) -> re-analyse -> exports, and is recorded
  in the PR.
* README feature list mentions pre-analysis where it lists live fact-checking.

**Implementation todos**

- [ ] Architecture/config/local-setup doc updates (new endpoints, env vars, lifecycle)
- [ ] README feature bullet
- [ ] End-to-end verification run on the dev stack, transcript in the PR
- [ ] Sweep earlier cards' docs debt (anything flagged in their reviews)

**Definition of Done**

- [ ] Docs accurate against the merged implementation (verify commands and paths)
- [ ] Lint/format clean where applicable
- [ ] No secrets or infrastructure identifiers committed
- [ ] PR opened against `dev`, CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff; resolve every correctness finding; address or
      justify quality findings; re-review after changes. Not Done until the review
      passes.
