# Video pre-analysis (durable one-shot fact-check)

An admin pre-analyses a ready imported video once, server-side: a background job extracts the
stored media's audio with ffmpeg and streams it through the exact live pipeline (AssemblyAI
streaming, then the configured fact-check path), and the full result - transcript, claims,
verdicts - is persisted durably in Postgres. Playback of a pre-analysed video never runs
transcription or an LLM again: subtitles are present from the first frame and highlight in sync
with playback, claims render from the stored verdicts, and a claim timeline strip under the player
marks where claims were checked, colored by verdict. Re-analysing at any time (the evidence corpus
evolves) is the same trigger; live fact-checking stays fully intact for live TV and for videos
that have not been pre-analysed.

This page is the operator guide: the lifecycle, the API, the live-vs-pre-analysed decision path,
operational characteristics, and the end-to-end verification runbook. The environment variables
are in [Configuration -> Video pre-analysis](configuration.md#video-pre-analysis).

## Lifecycle

Every video carries an `analysis_status`: `none | analysing | complete | failed`. The `analysing`
status doubles as the job lock, so two runs can never race on one video.

| Step | What happens |
|------|--------------|
| Trigger | `POST /api/videos/{id}/analyse` (admin). The video must be `ready` (there is media to analyse); the status flips to `analysing` and the trigger returns `202` while the run proceeds in the background. The same endpoint re-runs a `complete` or `failed` video. |
| Run | ffmpeg decodes the stored object to PCM, paced at `PREANALYSIS_PACING_FACTOR` x realtime (default 1.0 - AssemblyAI's streaming API rejects faster-than-realtime input, so a run takes about as long as the video). The audio flows through the same live analyzer a live viewer drives; every emitted event is teed and captured. |
| Progress | `analysis_progress_ms` (the audio position, accounted from delivered bytes so it advances smoothly through silence) is written every few seconds; `GET /api/videos/{id}/analysis` is the poll target, and both the backoffice row and the player chip render it. |
| Completion | The captured events are stored in `video_analyses` (one row per video) together with claim counters and an `engine` fingerprint (transcriber model, verifier provider and model, retrieval posture, pacing) recorded at run time, so the operator can see what produced a result before deciding to re-analyse. The video flips `complete`, `analyzed_at` is set, and `analysis_runs` increments. |
| Failure | Any interruption - a run-timeout overrun (`PREANALYSIS_RUN_TIMEOUT`), a transcription session dying mid-run, an ffmpeg decode error, a run that produced no events - flips the video `failed` with the reason in `analysis_error`. A failed run is always re-runnable. |
| Restart recovery | A backend restart mid-run flips any orphaned `analysing` video to `failed` at startup, never stuck `analysing`. Re-trigger to run it again. |
| Re-analyse | Allowed from `complete` and `failed` (and `none`). The previous stored result stays readable until the new run completes, then is overwritten atomically - a re-analysis in flight never blanks the player. |

With `PREANALYSIS_MAX_CONCURRENT` (default 1) runs already in flight, an accepted trigger queues:
the video holds `analysing` at zero progress until a slot frees.

## API

| Endpoint | Access | Behaviour |
|----------|--------|-----------|
| `POST /api/videos/{id}/analyse` | admin | Start or re-run the pre-analysis. `202` accepted; `404` unknown video; `409` a run is already in progress; `422` the video is not `ready`. |
| `GET /api/videos/{id}/analysis` | any authenticated | The lifecycle (`analysis_status`, `analysis_error`, `analyzed_at`, `analysis_runs`, `analysis_progress_ms`) plus, once a completed result is readable, `engine`, `counters` (total/credible/disputed/unverifiable), and `frames` - the whole stored session in the exact wire shapes the live WebSocket emits, with absolute video-time timestamps. |
| `GET /api/videos` | any authenticated | List items carry `analysis_status` and `analyzed_at` so tiles and the backoffice badge state without per-video calls. |
| `GET /api/videos/{id}/export/transcript.srt` / `.../export/claims.csv` | admin | SRT transcript and CSV decision trace, served from the stored analysis (Postgres first, then the 24 h Redis replay cache). |

## Live or pre-analysed: the decision path

- **`analysis_status` is `complete`** - the player hydrates over REST: it fetches the stored
  frames from `GET /api/videos/{id}/analysis` and folds them through the same reducers a live
  session uses. No live WebSocket is opened and no browser audio capture starts. The full
  transcript is on screen from the first frame of playback, the active subtitle highlights from
  the playback position, and the claim timeline strip renders under the player (it exists only on
  this path). Refresh-safe: reloading re-hydrates from the same stored result.
- **Any other status** - the video keeps today's live flow: the browser taps the `<video>`
  element's audio and streams it over the live WebSocket. If a 24 h Redis snapshot from a completed
  live view exists, the socket replays it instead of re-analysing (see
  [the analysis cache](configuration.md#analysis-cache-instant-replay)).
- **Live TV** (`/tv`) always uses the live pipeline; pre-analysis applies to stored library videos.

The storage seams line up as Postgres first, Redis second, live pipeline last: the live WebSocket's
replay fast-path and the export endpoints consult the durable stored analysis before the Redis
cache, so a pre-analysed video serves the stored result on every surface. A live viewer's completed
session still writes only the Redis cache - a lossy browser-driven view (seeks, partial views)
never overwrites a deliberate pre-analysis.

## Where the controls live

- **Backoffice -> Videos**: every row badges its analysis state (`analysing` shows a live
  percentage when the duration is known), completed rows show the completion date and claim
  counters, failed rows show the stored error. "Analyse" triggers a ready video's first run (or a
  failed one's retry) directly; "Re-analyse" on a completed video asks for a two-step confirm
  because it overwrites the stored result.
- **Player (`/app`)**: an admin sees a "Pre-analyse" control on a ready, un-analysed (or failed)
  video; everyone sees the progress chip while a run is in flight and the analysed chip once
  complete. Library tiles badge analysed videos.

## Operational characteristics

- **A pre-analysis shares the live analyzer.** Claim scoring runs in the same bounded worker pool
  live viewers use, so heavy simultaneous live-viewer load can shed some claim scoring inside a
  stored result: the affected statements store degraded results (skipped or instantly
  unverifiable), exactly as a live viewer would have seen them in that moment.
  `PREANALYSIS_MAX_CONCURRENT=1` (the default) mitigates by never adding more than one run's
  load, and a re-analyse in a quiet period recovers the shed claims.
- **A rare stale read can briefly mask a retry.** In the backoffice, a catalog poll that was
  already in flight when a retry-from-failed trigger fired can report the row as `failed` while
  the new run is actually live. Re-clicking "Analyse" resolves it: the backend answers `409` (a
  run is in progress) and the row flips back to `analysing`.

## End-to-end verification runbook

The full pass needs the live stack: real API keys (transcription and the configured LLM path) and
the local Docker stack. Steps 1-9 are the operator's; the repository's unit gates (Go and Vitest,
run on every PR) cover the underlying status codes, lifecycle transitions, hydration, and export
formatting without live infrastructure.

Prerequisites: `.env` carries `TRANSCRIPTION_API_KEY` and the LLM key for whatever fact-check path
you run (see [Configuration](configuration.md#llm-provider-and-the-fact-check-verify-path));
`make up` has the stack running and you are signed in as `admin` / `test1234`.

1. **Ingest.** Open `http://localhost:3000/backoffice` and upload a short video file (a 1-2 minute
   clip keeps the realtime-paced run short) or import a YouTube URL; wait for the row to reach
   `ready`.
2. **Trigger.** Click "Analyse" on the row. The badge flips to analysing and shows a live
   percentage once the first progress lands (indeterminate for a raw upload with no known
   duration).
3. **Watch the run.** The `/app` player for that video shows the progress chip to every signed-in
   user. The run takes about the clip's duration (realtime pacing).
4. **Analysed playback.** When the row shows complete (date + claim counters), open the video on
   `/app`: the full transcript is listed immediately, the active subtitle follows playback, and
   the claim timeline strip renders under the player. Click a marker: playback seeks to that
   claim. Verify in the browser dev tools (Network tab) that no `/live` WebSocket was opened for
   this video.
5. **Live path untouched.** Open a different, un-analysed ready video: the live WebSocket opens
   and analysis runs live as before.
6. **Re-analyse.** Back in the backoffice, click "Re-analyse" and confirm. While the new run is in
   flight, the previously stored result must still play on `/app`; on completion the row re-dates
   and the player serves the fresh result.
7. **Failure recovery.** Trigger a run, then `docker compose restart backend` mid-run. The row
   flips to failed with the interruption stored as its error; click "Analyse" to re-run to
   completion.
8. **Exports.** From the analysed video's player export controls (admin), download the SRT and the
   CSV; the SRT must match the stored subtitles and the CSV one row per checked claim. Or fetch
   them directly with a dev-realm token:

   ```bash
   TOKEN=$(curl -s http://localhost:8081/realms/truth-in-stream/protocol/openid-connect/token \
     -d grant_type=password -d client_id=truth-in-stream-web \
     -d username=admin -d password=test1234 | jq -r .access_token)
   curl -s -H "Authorization: Bearer $TOKEN" \
     "http://localhost:8080/api/videos/<video-id>/export/transcript.srt" | head
   curl -s -H "Authorization: Bearer $TOKEN" \
     "http://localhost:8080/api/videos/<video-id>/export/claims.csv" | head
   ```

9. **Lifecycle over the API** (optional, same token):

   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" \
     "http://localhost:8080/api/videos/<video-id>/analysis" \
     | jq '{analysis_status, analysis_runs, analyzed_at, counters}'
   ```
