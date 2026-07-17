# Imported-video pre-analysis: durable one-shot fact-check with timeline playback

Date: 2026-07-17
Status: draft for review (epic not yet created in Linear)

## Goal

Let the operator pre-analyse an imported video once, server-side, and persist the full
fact-check result in Postgres. Playback of a pre-analysed video never runs the live
pipeline again: subtitles are already generated and highlight in sync with playback,
claims render from stored verdicts, and a timeline strip shows where claims were checked,
colored by verdict. The backoffice can re-analyse a video at any time (the evidence
corpus evolves). Live fact-checking stays fully intact for live TV and for imported
videos that have not been pre-analysed.

## Where this sits

Today an imported video is analysed by the viewer's browser, per view: playback taps the
`<video>` element's audio (`stack/frontend/src/lib/live/audio-capture.ts`), resamples to
16 kHz s16le PCM, and streams it over `GET /api/videos/{id}/live` (WebSocket) into
`service.LiveAnalyzer` -> AssemblyAI streaming v3 -> the verify path. Nothing server-side
ever reads the stored media object. The only reuse mechanism is the 24 h Redis snapshot
cache (VER-142..146): on genuine completion the handler persists the ordered
`[]service.LiveEvent` as `analysis:v1:{video.ID}` and replays it on the next view through
the `AnalysisRecorder`/`AnalysisReplayer` ports in
`stack/backend/internal/handler/live.go`.

The closest precedent for everything this epic adds is the documents feature
(migration `0012_documents`): `analysis_status` as state machine and job lock,
spawn-goroutine analyzer with startup recovery (`service/document_analyzer.go`), admin
`POST /api/documents/{id}/reanalyse`, 2 s status polling on the frontend, and the exact
verdict column vocabulary in `document_claims`.

## Approaches considered

1. **Headless server-side run through the existing live pipeline, persisted in Postgres
   (chosen).** A backend job extracts audio from the stored object with ffmpeg (already
   in both backend images for yt-dlp), streams it through the same
   StreamSegmenter -> AssemblyAI -> verify path used live, tees the emitted events, and
   persists them durably. Reuses the whole analysis stack and the snapshot seam;
   transcription and verdict behavior are identical to live.
2. **Promote the Redis snapshot to Postgres after a completed live view.** No headless
   runner, smallest diff. Rejected: the analysis would depend on a human watching the
   entire video without seeking, there is no button and no re-analyse without a full
   rewatch, and partial views persist nothing.
3. **AssemblyAI async (batch) transcription for the pre-analysis path.** Faster than
   realtime. Rejected: VER-43 deliberately retired the batch path and made the realtime
   diarizing WebSocket the single transcriber; reintroducing batch forks segmentation and
   diarization behavior between live and pre-analysed content and doubles the
   transcription surface to maintain.

## Design

### Data model (migration 0019)

Mirror the documents pattern.

`videos` gains analysis lifecycle columns (exactly like `documents`):

- `analysis_status text NOT NULL DEFAULT 'none'` - `none | analysing | complete | failed`
  (`analysing` doubles as the job lock, claimed with a conditional UPDATE)
- `analysis_error text`
- `analyzed_at timestamptz`
- `analysis_runs int NOT NULL DEFAULT 0`
- `analysis_progress_ms bigint NOT NULL DEFAULT 0` (audio position, drives the progress %)

New table `video_analyses`, one row per video (re-analysis overwrites via upsert; history
is the `analysis_runs` counter, not rows):

- `video_id uuid PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE`
- `snapshot_version int NOT NULL` (reuse `service.SnapshotVersion`)
- `events jsonb NOT NULL` - the ordered `[]service.LiveEvent`, same payload the Redis
  snapshot stores today; timestamps are absolute video time (job streams from 0)
- `engine jsonb NOT NULL` - model identifiers and config fingerprint (transcriber model,
  verifier model, retrieval settings) recorded at run time, so the operator can see what
  produced a result before deciding to re-analyse
- `claims_total int NOT NULL`, `claims_credible int NOT NULL`,
  `claims_disputed int NOT NULL`, `claims_unverifiable int NOT NULL` - denormalized
  counters for list badges without loading the blob
- `created_at`, `updated_at`

No separate per-claim projection table: the player needs the full event list anyway (one
fetch hydrates subtitles, claims, and timeline), exports reconstruct from `events`, and
badges use the counters. Add a claims table only when a real SQL-over-claims need
appears.

### Backend: persistence seam

- `PostgresSnapshotStore` in `internal/store/postgres` implementing durable
  `Persist`/`Snapshot` over `video_analyses` (sqlc queries in
  `queries/video_analyses.sql`).
- The live handler's `AnalysisReplayer` becomes a composite: Postgres (permanent,
  pre-analysed) first, then Redis (24 h, live-view replays), then miss -> live pipeline.
  The `AnalysisRecorder` used by live views stays Redis-only: a browser-driven view is
  lossy (seeks, partial views) and must not overwrite a deliberate pre-analysis.
- Export endpoints (`/api/videos/{id}/export/transcript.srt|claims.csv`) already read
  through `AnalysisReplayer`, so they serve pre-analysed content with no change beyond
  the composite wiring.

### Backend: pre-analysis job

`VideoAnalyzer` service mirroring `DocumentAnalyzer`:

- `Start(videoID)`: video must be `ready`; claim the lock by flipping
  `analysis_status -> analysing` with a conditional UPDATE (409 on conflict), reset
  progress, then `spawn` a goroutine on a detached, timeout-bounded context.
- The run: presign/open the stored object via `domain.MediaStore`, exec ffmpeg
  (new `internal/audioextract` adapter, same exec pattern as `internal/ytdlp`) to decode
  to 16 kHz s16le mono PCM on stdout, chunk to ~100 ms frames (AssemblyAI rejects frames
  outside 50-1000 ms), pace at realtime by default with a configurable
  `PREANALYSIS_PACING_FACTOR` (verify AssemblyAI streaming's tolerance for
  faster-than-realtime submission against current docs during delivery; correctness
  first, speed second), and feed `LiveAnalyzer.Run` exactly as the live handler does.
- Tee all emitted events; update `analysis_progress_ms` periodically; on pipeline flush,
  upsert `video_analyses` (events + engine + counters) and flip `complete`/`analyzed_at`
  /`analysis_runs`; on error flip `failed` with `analysis_error`. Terminal writes happen
  on a separate short-timeout context.
- `Recover()` on startup marks orphaned `analysing` rows `failed` (restart-safe,
  re-runnable), like `RecoverInterruptedAnalyses`.
- Global concurrency cap `PREANALYSIS_MAX_CONCURRENT` (default 1) via semaphore; queued
  starts hold the `analysing` status with zero progress until a slot frees.
- Re-analyse is the same entry point: allowed from `complete` and `failed` (and `none`);
  the previous analysis stays readable until the new run completes, then is overwritten
  atomically. On success no Redis invalidation is needed - Postgres is consulted first.

### API

- `POST /api/videos/{id}/analyse` (RequireAdmin) - start or re-run; 202 on accept, 409
  while `analysing`, 422 unless the video is `ready`. One endpoint for both first run and
  re-analysis.
- `GET /api/videos/{id}/analysis` (any authenticated) - `{analysis_status,
  analysis_error, analyzed_at, analysis_runs, progress, engine, counters, frames}` where
  `frames` is the wire-shaped event list (the same JSON the WS serializer emits), present
  only when `complete`. Poll target while `analysing`.
- `GET /api/videos` list items gain `analysis_status` (+ `analyzed_at`) so tiles and the
  backoffice list can badge state without extra calls.

### Frontend: analysed playback (in `/app`)

- On selecting a video whose `analysis_status` is `complete`, fetch
  `GET /api/videos/{id}/analysis` and hydrate the existing live-analysis store by running
  the fetched frames through the existing frame reducers with base time 0 - timestamps
  are absolute video time. Do not open the live WebSocket and do not start audio capture
  for these videos. (The WS `prepareFrame` shift by socket-open position would corrupt
  absolute replay timestamps on a mid-video open; REST hydration sidesteps that and is
  refresh-safe.)
- Everything downstream is unchanged and free: `LiveStatementList` already highlights the
  active subtitle from `currentTime` via binary search, and the claims/summary/speaker
  components read the same store. The full transcript is present from the first frame of
  playback - "subtitles already generated, highlighted in real time as the video goes
  by".
- Videos without a completed analysis keep today's live WS flow untouched.
- Admin-only "Pre-analyse" affordance on the player/library view when
  `analysis_status` is `none`/`failed`; while `analysing`, a progress chip polling the
  analysis endpoint (2 s cadence, like documents). Non-admins see status only.
- Library tiles show an "Analysed" badge from the list payload.

### Frontend: claim timeline

Net-new UI - no custom seek bar exists (the player uses native controls). Render a
timeline strip aligned to `duration` directly under the player (do not rebuild the
scrubber): one marker/segment per checked claim, positioned by its statement's
`[start,end]`, colored by verdict with the existing verdict palette - credible and
disputed prominent, unverifiable muted. Hover reveals the claim text and verdict;
click seeks via the playback store. The strip renders only when the video is
pre-analysed, from the hydrated store. Dense clusters of markers are the "hot moments"
signal; no separate heat computation in v1.

### Frontend: backoffice

In the videos section: an analysis status column/badge (`none`, `analysing` with %,
`complete` with date and claim counters, `failed` with error), an "Analyse" action for
ready videos, and a two-step "Re-analyse" (destructive-ish: overwrites the stored result)
for completed ones. Reuse the section's existing polling rhythm while any row is
`analysing`.

### What does not change

- Live TV (`/api/tv/channels/{id}/live`) and live analysis of non-pre-analysed imported
  videos: untouched.
- The WS wire contract and frame types: untouched (REST payload reuses the same frame
  shapes).
- The Redis cache keeps its current role for live-view replays; Postgres simply wins for
  pre-analysed videos.
- Keycloak, roles, ingestion: untouched. The analyse trigger is admin-gated like the
  rest of ingestion.

## Testing

- Go (table-driven, `-race`): lock claim/conflict transitions, recovery of orphaned
  runs, ffmpeg adapter (fake exec), pacing/chunking bounds, composite replayer
  precedence (Postgres over Redis over miss), upsert-with-counters, handler status
  codes, list payload flag.
- Frontend (Vitest): REST hydration produces the same store state as the equivalent WS
  session; no socket/capture opens for analysed videos; timeline positioning, verdict
  coloring, and click-to-seek; analyse button state machine; backoffice badge/actions.
- End-to-end check on the dev stack: ingest a short sample, pre-analyse, replay with
  subtitles + timeline, re-analyse from backoffice; verify SRT/CSV exports serve the
  stored analysis.

## Assumptions taken (flag if wrong)

1. Pre-analysis runs inside the API server process like `DocumentAnalyzer` - no queue,
   no worker binary. Realtime pacing means a 2 h video takes about 2 h to pre-analyse at
   factor 1.0; acceptable for an operator-triggered background job, tunable later.
2. Re-analysis overwrites; no stored history of prior runs beyond `analysis_runs` and
   `engine` metadata.
3. The trigger is admin-only (ingestion is a backoffice operation); every authenticated
   user gets the analysed playback and timeline.
4. Timeline is a separate strip under the native controls, not a rebuilt scrubber.
5. "Hot moments" = visible density of colored claim markers, not a computed heat score.

## Cards (to create in Linear, label `epic:preanalysis`)

Five cards, one owning agent, sequenced `D1 -> D2 -> {D3, D4} -> D5`. Full card bodies
live in the epic entry in the roadmap plans. Summary:

- **D1 - Backend: durable analysis storage and read API.** Migration 0019, store +
  sqlc, composite replayer wiring, `GET /api/videos/{id}/analysis`, list flag.
- **D2 - Backend: headless pre-analysis job.** `internal/audioextract` (ffmpeg),
  `VideoAnalyzer` (lock, spawn, recover, cap, pacing), `POST /api/videos/{id}/analyse`,
  export verification. Depends on D1.
- **D3 - Frontend: analysed playback + claim timeline.** REST hydration, WS/capture
  suppression, pre-analyse button + progress, timeline strip, library badge. Depends on
  D2.
- **D4 - Backoffice: analysis status + analyse/re-analyse controls.** Depends on D2;
  file-disjoint from D3.
- **D5 - Docs close-out + end-to-end verification.** Depends on D3 + D4.
