# Pending Linear cards - Epic D (video pre-analysis)

Staged card bodies for the `epic:preanalysis` cards. The Linear connector was not
authenticated when this epic was drafted; create these five cards verbatim (team
Veroveit, project Truth in Stream, state `Todo`, label `epic:preanalysis`, priorities as
noted), record the assigned VER-IDs in `EPICS-TRUTH-IN-STREAM.md`, re-run `/roadmap`,
then delete this file.

Dependencies to record: D2 depends_on D1; D3 depends_on D2; D4 depends_on D2;
D5 depends_on D3, D4.

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
`stack/backend/internal/domain/document.go`). The pre-analysis job (next card) writes
through the store added here; the frontend cards read the API added here. No job, no
frontend, no ffmpeg in this card.

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

## D2 - Headless server-side pre-analysis job for imported videos (priority: High, depends_on: D1)

**Outcome**

The operator triggers one server-side analysis of a ready imported video and can re-run
it later when the evidence corpus has changed. The job transcribes and fact-checks the
stored media without any browser involved, persists the result durably, reports
progress, and survives restarts. Once a video is analysed, opening it never spends
transcription or LLM budget again.

**Context**

Second backend card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); requires the storage
and read API card (D1) to be merged or in review. Today imported-video analysis is
browser-driven per view: the client streams playback audio to
`GET /api/videos/{id}/live` -> `service.LiveAnalyzer` -> AssemblyAI streaming v3 ->
verify path. Nothing reads the stored object server-side. `DocumentAnalyzer`
(`stack/backend/internal/service/document_analyzer.go`) is the job-lifecycle precedent:
conditional-update lock, injectable `spawn`, detached timeout-bounded context, terminal
writes on a short separate context, startup recovery. ffmpeg is already installed in
both backend images (yt-dlp dependency). Live TV and non-analysed imported videos keep
the live pipeline untouched.

**Approach**

New `stack/backend/internal/audioextract` adapter following the `internal/ytdlp` exec
pattern: ffmpeg decodes the stored object (presigned URL or piped stream from
`domain.MediaStore`) to 16 kHz s16le mono PCM on stdout. Chunk to ~100 ms frames -
AssemblyAI closes with 3007 on frames outside 50-1000 ms - and pace submission:
realtime (factor 1.0) default, `PREANALYSIS_PACING_FACTOR` to go faster only after
verifying AssemblyAI streaming's documented tolerance for faster-than-realtime input
against current docs; correctness over speed. `VideoAnalyzer` service mirrors
`DocumentAnalyzer`: `Start` requires video status `ready`, claims the
`analysis_status='analysing'` lock (409 on conflict), spawns the run; the run feeds
`LiveAnalyzer.Run` exactly as the live handler does, tees emitted events, updates
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

- [ ] `internal/audioextract`: ffmpeg exec adapter -> 16 kHz s16le mono PCM stream,
      with fake-exec unit tests
- [ ] Chunking/pacing component (~100 ms frames, configurable factor) with tests on
      frame-size bounds
- [ ] `VideoAnalyzer` service: lock claim, spawn, run, progress, terminal writes,
      `Recover()` wired at startup in `cmd/server/main.go`, concurrency semaphore
- [ ] Engine metadata + claim counters computed from the teed events at completion
- [ ] `POST /api/videos/{id}/analyse` handler (RequireAdmin) + route registration
- [ ] Config: `PREANALYSIS_MAX_CONCURRENT`, `PREANALYSIS_PACING_FACTOR`, run timeout;
      forwarded in `docker-compose.yml`; documented in the config reference
- [ ] Verify SRT/CSV exports and the WS fast-path serve the job's stored analysis
- [ ] Table-driven tests: lifecycle transitions, recovery, 409/422 paths, pacing bounds

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

## D3 - Analysed playback: stored subtitles, claims, and verdict timeline in the player (priority: High, depends_on: D2)

**Outcome**

Opening a pre-analysed video feels instant and complete: the full transcript is there
before playback starts and the active line highlights as the video plays, claims and
verdicts render from the stored result, and a timeline strip under the player shows
where claims were checked, colored by verdict, with click-to-seek. The operator can
trigger a pre-analysis right from the player, and library tiles show which videos are
already analysed. No transcription or fact-checking runs during playback of an
analysed video.

**Context**

Frontend player card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); consumes the read API
(D1) and the job trigger (D2). The watch screen is
`stack/frontend/src/app/app/_components/library-experience.tsx` hosting
`PlaybackProvider`, `LiveAnalysisProvider`, `VideoPlayer` (react-player, native
controls) and the live panels. `use-live-analysis.ts` currently always opens the WS on
play and shifts frame timestamps by the playback position at socket open
(`prepareFrame`/`baseTime`) - stored frames carry absolute video time, so hydrating
over the WS would corrupt timestamps on a mid-video open; that is why analysed videos
hydrate over REST and never open the socket. `LiveStatementList` already highlights the
active segment from `currentTime` (binary search in `lib/fact-check/segments.ts`).
There is no existing seek-bar UI; the timeline strip is net-new and must not rebuild
the native scrubber. Backoffice UI is a separate card; do not touch
`stack/frontend/src/app/backoffice/*`.

**Approach**

Respect Server Components by default; `'use client'` only at the leaves that need it.
On selecting a video with `analysis_status === 'complete'` (from the list payload),
fetch `GET /api/videos/{id}/analysis` and hydrate the existing live-analysis store by
running the returned frames through the existing reducers in `lib/live/*` with base
time 0; skip `useLiveAnalysis` socket + `createMediaElementCapture` entirely for these
videos. Non-analysed videos keep the live flow byte-for-byte. Timeline: a strip
component aligned to `duration` under the player, markers/segments positioned by each
claim's statement span, colored with the existing verdict palette
(`live-claim-verdict.tsx`): credible and disputed prominent, unverifiable muted; hover
shows claim text + verdict; click seeks through the playback store; rendered only when
analysed. Admin-only pre-analyse button (status from the identity already exposed to
the app shell) when status is `none`/`failed`, calling `POST /api/videos/{id}/analyse`;
while `analysing`, poll the analysis endpoint every 2 s and show progress; non-admins
see a status chip only. Library tiles get an "Analysed" badge from the list payload.
Match the existing app copy conventions for all new strings.

**Acceptance criteria**

* A pre-analysed video shows its full transcript immediately; the active subtitle
  highlights in sync with playback, including after seeking; no WebSocket opens and no
  audio capture starts.
* The timeline strip marks every checked claim at its time span, colored by verdict;
  clicking a marker seeks the player; hovering identifies the claim.
* An admin sees and can use the pre-analyse button on a ready, un-analysed video and a
  live progress indicator while it runs; a non-admin cannot trigger analysis.
* Videos without a stored analysis keep the current live-analysis experience
  unchanged.

**Implementation todos**

- [ ] Analysis fetch + store hydration path (frames -> existing reducers, base time 0)
- [ ] Gate in the player wiring: analysed -> REST hydration; otherwise live WS
      (unchanged)
- [ ] Timeline strip component: positioning math, verdict colors, hover, click-to-seek
- [ ] Pre-analyse button + polling progress chip; admin gating; failed-state retry
- [ ] "Analysed" badge on library tiles from `analysis_status`
- [ ] Vitest: hydration equivalence with a WS session fixture, no-socket/no-capture
      assertion for analysed videos, timeline positioning/coloring/seek, button state
      machine, badge rendering

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

## D4 - Backoffice: video analysis status and analyse/re-analyse controls (priority: High, depends_on: D2)

**Outcome**

From the backoffice the operator sees at a glance which videos are analysed, when, and
with what result volume, and can analyse a new video or re-analyse an old one after the
evidence corpus has changed - with a confirmation step, since re-analysing overwrites
the stored result.

**Context**

Backoffice card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); consumes the read API
(D1) and trigger endpoint (D2). The videos section lives in
`stack/frontend/src/app/backoffice/_components/backoffice-videos-section.tsx` +
`backoffice-video-list.tsx` (kind/status badges, two-step delete, 2.5 s polling for
pending YouTube ingests). The documents reanalyse control
(`stack/frontend/src/app/documents/_components/reanalyse-control.tsx`) is the trigger
precedent (409/conflict handling). File-disjoint from the player card: this card
touches only `stack/frontend/src/app/backoffice/*` and the shared video API client.

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

## D5 - Pre-analysis docs and end-to-end verification close-out (priority: Medium, depends_on: D3, D4)

**Outcome**

The feature is documented and proven end to end: an operator following the docs can
ingest a video, pre-analyse it, watch it with stored subtitles and the verdict
timeline, re-analyse from the backoffice, and export the results - and the docs
explain when live analysis still applies.

**Context**

Close-out card of the video pre-analysis epic (design:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`); starts once the
player and backoffice cards are merged or in review. Documentation lives under
`docs/` (architecture, config reference, local setup); the config reference gained an
ingestion refresh recently - extend, do not fork, its structure.

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
