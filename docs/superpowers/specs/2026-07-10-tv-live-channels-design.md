# Live TV Channels - Design

Real-time fact-checking of French TV news channels: a capture worker ingests free,
legally accessible live streams, the backend analyses them through the existing live
pipeline, every captured hour is archived to S3 as a replayable recording, viewers
follow along on a new `/tv` page, and admins turn channels on and off from a simple
management surface.

Designed 2026-07-10. Autonomous-mode design: clarifying questions were replaced by
the documented assumptions in section 1; review them first.

## Decisions

- **Sources are the free, non-DRM paths only.** TF1+/LCI, france.tv (France 2/3 JT)
  and M6+ players are Widevine-DRM'd; circumvention is illegal in France
  (Art. L331-5 CPI) and out of scope permanently, not just for v1. The usable
  sources are the official 24/7 YouTube live simulcasts (franceinfo, France 24 FR,
  BFMTV, Euronews FR, LCP, Public Senat; CNEWS and LCI to be verified during
  implementation) and the parliamentary video portals (videos.assemblee-nationale.fr,
  videos.senat.fr), which are the legally cleanest and best aligned with the
  political fact-check mission.
- **Capture is server-side and reuses the existing analysis pipeline unchanged.** A
  `tvcapture` worker acts as a headless viewer: it resolves and captures the stream,
  pipes 16 kHz mono PCM into a new per-channel live hub over WebSocket (the same
  audio contract the browser uses today), and the backend runs one `LiveAnalyzer`
  session per live channel, fanning events out to any number of viewers.
- **Archive lands as ordinary `videos` rows.** Hour-chunked segments are recorded as
  MPEG-TS (crash-tolerant), remuxed to MP4 on close, uploaded via presigned PUT to
  `recordings/{channel}/...`, and registered as `videos` with a new kind `tv`.
  Replay and re-analysis then reuse the existing watch/analysis flow with zero new
  analysis code: opening a recording streams its audio through
  `/api/videos/{id}/live` exactly like an imported YouTube video.
- **Channel on/off is a DB-backed admin toggle, reconciled by the worker.** New
  `tv_channels` table + admin CRUD API. The worker polls the channel list and
  reconciles running capture processes with the enabled set, so the toggle is the
  single control surface end to end.
- **Channel management mounts as a TV section in the backoffice.** The backoffice
  epic (VER-205..209, design
  `docs/superpowers/specs/2026-07-10-backoffice-admin-video-ingestion-design.md`)
  ships a `/backoffice` area with admin gate and navigation (VER-206) that sibling
  cards fill with sections; TV channel management follows that pattern and depends
  on VER-206. The `/tv` page itself stays a consumption surface for all
  authenticated users, consistent with the backoffice epic's split.
- **Tooling:** streamlink (8.4.0) resolves YouTube lives (isolates PO-token and
  bot-check churn, built-in retry), piped into one supervised ffmpeg (8.1.x) process
  with two outputs: PCM for STT, stream-copied segmented TS for archive. Plain HLS
  sources point ffmpeg at the manifest directly. A `bgutil-ytdlp-pot-provider`
  sidecar supplies PO tokens.
- **Legal posture is explicit.** Official YouTube embeds on the `/tv` page are
  sanctioned; continuous recording of YouTube lives conflicts with YouTube ToS even
  for free official channels. Archiving is therefore per-channel opt-in
  (`archive_enabled`), retention is short by default (30 days, app-enforced), the
  bucket stays private, and the parliamentary portals are the recommended archive
  sources. The operator owns the decision per channel; the system makes the safe
  configuration the default.

## 1. Assumptions (review these)

1. The named DRM'd broadcasters (TF1, France 2, M6) are wanted as *content*, not as
   *specific integrations*; the mission is served by the free news channels and
   parliamentary sources that cover the same political material. If a paid/partner
   route to DRM'd JT content is ever wanted, that is a separate negotiation, not an
   engineering card.
2. "Turn on a channel" means: capture + live analysis + (if archive_enabled)
   recording all run while ON; everything stops when OFF. LLM/STT cost control is
   the toggle itself; no per-viewer gating in v1.
3. Live analysis runs while a channel is ON even with zero viewers - the archive and
   the fact-check record are the product, not just the live audience.
4. The new page lives at `/tv` inside the authenticated app, alongside the existing
   video (`/app`) and documents (`/documents`) areas, and joins the navigation added
   by VER-180.
5. One production capture host is enough for the initial channel count (each channel
   is one ffmpeg stream-copy process; CPU is dominated by the single PCM decode).
6. Recordings do not need frame-accurate alignment with the live verdict feed in v1;
   a recording re-analysed later produces fresh verdicts through the normal path.

## 2. Sources and legal posture

Seed registry (all `enabled=false`; the operator flips them deliberately):

| Channel | Kind | Source | Notes |
|---|---|---|---|
| franceinfo | youtube | youtube.com/franceinfo live | Confirmed 24/7 official live |
| France 24 (FR) | youtube | youtube.com/@FRANCE24 live | Confirmed 24/7 official live |
| BFMTV | youtube | youtube.com/@BFMTV live | Confirmed 24/7 official live |
| Euronews (FR) | youtube | youtube.com/c/euronewsfr/live | Confirmed 24/7 official live |
| LCP | youtube | official channel live tab | Political debates; verify continuity |
| Public Senat | youtube | official channel live tab | Political debates; verify continuity |
| CNEWS | youtube | @CNEWSofficiel streams tab | Verify 24/7 continuity before enabling |
| LCI | youtube | @LCI live tab | Verify 24/7 continuity before enabling |
| Assemblee nationale | hls | videos.assemblee-nationale.fr | Legally cleanest; verify stable manifest |
| Senat | hls | videos.senat.fr/direct | Legally cleanest; verify stable manifest |

Legal notes (not legal advice): short-quotation (Art. L122-5 CPI) does not cover
bulk archiving of full broadcasts; YouTube ToS prohibits stream-ripping even of
official free channels while explicitly sanctioning embeds; parliamentary content
carries a constitutional public-broadcast mandate and an Etalab open-licence
precedent (video-specific terms to confirm). Consequences baked into the design:
private bucket, per-channel `archive_enabled` opt-in, short default retention,
official YouTube embed (never a re-hosted stream) as the on-page player, and the
source inventory doc records the licence posture per channel (extends the
VER-204 inventory).

## 3. Architecture

```
tvcapture worker (EC2 ingestion host / local compose)
  reconcile loop <-- GET /api/tv/channels (poll ~30s)
  per enabled channel, supervised:
    streamlink (youtube) | direct URL (hls)
      -> ffmpeg (single process, two outputs)
           -> PCM s16le 16k mono --WS--> backend feed endpoint
           -> segment .ts hourly --on close: remux .mp4
                -> presigned PUT to S3 recordings/{slug}/...
                -> POST register recording
backend
  tv hub (internal/service): one live session per channel
    feed WS (publisher, service auth) -> LiveAnalyzer.Run -> event ring buffer
    viewer WS fan-out (any authed user, late-join backlog)
  channels CRUD (admin), recordings registration -> videos kind 'tv'
frontend
  /tv page: channel grid -> channel view
    official YouTube embed (youtube kind) + live subtitle/verdict panels (reused)
    recordings strip -> existing watch experience
    admin-only: toggles, add/edit channel, capture status
```

Failure behaviour: the worker supervises each pipeline with a stall watchdog and
restarts with backoff, re-resolving the source URL each time; a restart gap is
accepted (live-only feeds cannot be seeked back). A crash leaves a partial `.ts`
segment which is remuxed and uploaded at next startup ("salvage pass"). If the
backend or hub connection drops, capture and archiving continue; the feed WS
reconnects with backoff (analysis has a gap, archive does not). If AssemblyAI or
the LLM path degrades, the existing LiveAnalyzer behaviour applies unchanged.

## 4. Data model (migration `0015_tv_channels`)

```sql
CREATE TABLE tv_channels (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            text NOT NULL UNIQUE,
    name            text NOT NULL,
    source_kind     text NOT NULL CHECK (source_kind IN ('youtube', 'hls')),
    source_ref      text NOT NULL,
    enabled         boolean NOT NULL DEFAULT false,
    archive_enabled boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE videos ADD COLUMN channel_id uuid REFERENCES tv_channels(id) ON DELETE SET NULL;
ALTER TABLE videos ADD COLUMN recorded_at timestamptz;
```

`videos.kind` gains `tv` (domain enum + checks). Recording titles are generated
("franceinfo - 2026-07-10 20:00"). Retention is app-enforced: a daily scheduled job
(existing `internal/schedule` cron) deletes recordings older than
`TV_RECORDING_RETENTION_DAYS` (default 30) - S3 object and row together, so the
gallery never shows dead links. No S3 lifecycle dependency (avoids row/object
drift); a lifecycle rule on `recordings/` at retention + grace is an optional
terraform backstop.

Channel live status is not persisted: the hub knows whether a publisher feed is
connected and the handler enriches the channel list response with `live: true/false`.

## 5. API surface

| Route | Method | Auth | Purpose |
|---|---|---|---|
| `/api/tv/channels` | GET | any authed | List channels + live status (drives `/tv` and worker reconcile) |
| `/api/tv/channels` | POST | admin | Create channel |
| `/api/tv/channels/{id}` | PATCH | admin | Edit / toggle `enabled`, `archive_enabled` |
| `/api/tv/channels/{id}` | DELETE | admin | Remove channel (recordings kept, FK nulls) |
| `/api/tv/channels/{id}/feed` | GET (WS) | service (admin role) | Publisher: PCM frames in, control JSON out; single publisher per channel |
| `/api/tv/channels/{id}/live` | GET (WS) | any authed | Viewer fan-out: LiveEvents + late-join backlog |
| `/api/tv/recordings/uploads` | POST | service | Presigned PUT for a closed segment (reuses `PresignUploadOnce`) |
| `/api/tv/recordings` | POST | service | Register uploaded segment -> `videos` row kind `tv` |

Worker auth: a dedicated Keycloak client `tv-capture` (client-credentials service
account) carrying the `admin` realm role; WS auth via the existing `?access_token=`
query-param path. No new middleware concept - `RequireAdmin` already covers it.
Recordings list per channel comes from the existing videos listing filtered by
`channel_id` (extend the videos query, no new endpoint).

## 6. Backend: tv hub and capture worker

**Hub** (`internal/service/tvhub.go`): per-channel session keyed by channel id. On
publisher connect: start `LiveAnalyzer.Run(ctx, pcm)` (the existing service, French
bias, diarization - untouched), buffer the last N events (ring, ~200), broadcast to
subscribers, drop slow subscribers rather than block the session. On publisher
disconnect: close the session and notify viewers (`off_air` event). One publisher
per channel; a second feed connection is rejected. The existing per-video replay
cache is not used for channel sessions (streams are unbounded; recordings carry the
replay story instead).

**Worker** (`cmd/tvcapture`, `internal/tvcapture/`): follows the existing worker cmd
pattern. Reconcile loop diffs enabled channels against running captures. Per
channel, a supervisor owns the process tree (streamlink | ffmpeg), a stall watchdog
(no PCM bytes or no segment progress for T seconds -> kill and restart with
backoff), the segment closer (remux TS->MP4 with `-c copy`, presign, PUT, register,
delete local), and the startup salvage pass. ffmpeg invocation shape:

```
ffmpeg -i pipe:0
  -map 0:a -f s16le -ar 16000 -ac 1 pipe:1
  -map 0:v? -map 0:a -c copy -f segment -segment_time 3600 -strftime 1
  -segment_format mpegts /work/{slug}/%Y%m%d_%H%M%S.ts
```

S3 layout: `recordings/{channel_slug}/{YYYY}/{MM}/{DD}/{HHMMSS}.mp4` in the existing
media bucket. New env (all parsed in `internal/config`): `TV_CAPTURE_ENABLED`,
`TV_CAPTURE_CLIENT_ID/SECRET`, `TV_SEGMENT_SECONDS` (default 3600),
`TV_RECORDING_RETENTION_DAYS` (default 30), `TV_FEED_STALL_SECONDS`. The worker
image adds ffmpeg and streamlink; a `potprovider` compose sidecar supplies PO
tokens to streamlink for YouTube sources.

## 7. Frontend: `/tv` page

Routes: `src/app/tv/page.tsx` (+ `_components/`), mirroring the documents area
structure. Joins the app navigation (Videos / Documents / TV) added by VER-180.

- **Channel grid**: tile per channel with name, ON AIR badge (from `live` status),
  and for admins the enable toggle. Disabled channels are visible to admins only.
- **Channel view**: for `youtube` channels, the official YouTube iframe embed as the
  player (sanctioned, zero stream re-hosting); for `hls` channels v1 shows no
  player, only the live feed panels (an hls.js player is a follow-up if CORS
  allows). Beside/below the player: the existing live components - subtitle strip,
  fact-check panel, claim list, speaker credibility - driven by a new
  `use-channel-live` hook that consumes the read-only viewer WS into the existing
  `live-analysis-store`. No audio capture, no playback pacing: events render as
  they arrive. A visible "analysis runs ~Xs behind the embed" note manages the
  offset expectation.
- **Recordings strip**: the channel's recordings (videos kind `tv`), newest first,
  linking into the existing watch experience for replay/re-analysis (deep link into
  the app area's selected-video state; small wiring addition).
- **Channel management (backoffice section)**: a TV channels section on
  `/backoffice` (scaffold from VER-206) - enable / archive toggles per channel,
  add/edit channel form (name, kind, source ref), delete, and capture status (live,
  last recording at). Deliberately minimal: one section, table plus form, following
  the same section pattern the backoffice epic uses for video and document
  ingestion. `/tv` shows admin-visible state (disabled channels greyed) but carries
  no management controls.

## 8. Infra and ops

- **Runtime**: a `tvcapture` service in `docker-compose.ingest.yml` (plus dev
  compose for local runs), deployed on the existing EC2-ingestion-host pattern over
  SSM. Terraform reuses `modules/ingestion-host` (new instance or a var on the
  module); capture host sizing is modest (stream-copy, one PCM decode per channel).
  All applies remain human-gated as usual.
- **Secrets/config**: Keycloak `tv-capture` client secret and TV_* env via SSM
  parameters, mirroring existing host wiring. Nothing infrastructure-identifying in
  the tree (public repo).
- **Observability**: worker logs per channel; a Slack run-alert on capture death
  after retry exhaustion (existing `SLACK_WEBHOOK_URL` pattern); recording
  registration failures alert rather than silently dropping segments.
- **Docs**: `docs/tv-live.md` (operator guide: enabling a channel end to end,
  legal posture per source class, salvage/retention behaviour) plus configuration
  reference entries; extends the VER-204 source inventory with the TV channels and
  their licence posture.

## 9. Epic breakdown

Six cards, dependency-ordered: A first, then B; C and D in parallel after B
(D also waits on the backoffice navigation foundation to avoid colliding on the
shared nav component); E after A plus the backoffice foundation; F after C.

- **A. TV channel registry: schema, admin API, seeds** - migration 0015, domain +
  store + service + handler CRUD, seed registry (all disabled), videos kind `tv`.
- **B. Channel live hub: publisher feed, viewer fan-out, per-channel analyzer
  session** - feed WS, viewer WS with backlog, LiveAnalyzer session lifecycle,
  live-status enrichment. Depends on A.
- **C. tvcapture worker: capture, archive, register** - reconcile loop, supervisor
  + watchdog, streamlink/ffmpeg pipeline, segment close -> remux -> presign ->
  upload -> register, salvage pass, retention job, compose wiring, ffmpeg/streamlink
  in the worker image, PO-token sidecar. Depends on A and B.
- **D. `/tv` page: grid, live view, recordings** - channel grid, channel view with
  embed + reused live panels via `use-channel-live`, recordings strip + deep link,
  navigation entry. Depends on A, B, and the backoffice foundation card (VER-206,
  shared navigation files).
- **E. Backoffice TV channels section** - toggles, add/edit/delete, capture status
  on `/backoffice`. Depends on A and VER-206.
- **F. Capture infra and operator docs** - terraform host wiring, SSM plumbing,
  Slack alerts, `docs/tv-live.md`, source inventory extension. Depends on C.

Every card carries the standard gates: tests with the change, e2e check of the
card's surface, code review, rebase, green CI, merge. The epic-end
maintaining-documentation pass folds into F.

Card tracking (2026-07-10): A = VER-210, B = VER-211, C = VER-212. D, E, and F
could not be created because the Linear workspace hit its issue limit; create
them from this section (D blocked by VER-210, VER-211, VER-206; E blocked by
VER-210, VER-206; F blocked by VER-212) once capacity is freed.

## 10. Testing

- Registry/API: table-driven handler + store tests (CRUD, role gating, slug
  uniqueness, kind checks); seed idempotency.
- Hub: session lifecycle tests with a fake analyzer (single publisher enforcement,
  fan-out, late-join backlog, slow-subscriber drop, off_air on disconnect), race
  (`go test -race`).
- Worker: supervisor tests with a fake process runner (reconcile diffs, watchdog
  restart with backoff, salvage pass); segment-close pipeline against MinIO
  (remux -> upload -> register); fixtures use real wire shapes (registration
  payloads match the handler contract).
- Frontend: Vitest for the grid/view state, `use-channel-live` store integration,
  admin gating; the e2e check drives a real capture of a short local HLS fixture
  stream through compose (per the e2e-real-entrypoint rule: the delivered compose
  service, not `go run`).
- Cost note for e2e: live-path e2e uses a local fixture stream (ffmpeg-generated
  test HLS), never a real YouTube capture in CI.

## 11. Risks and mitigations

- **YouTube capture churn (PO tokens, bot checks)** - streamlink + PO-token sidecar
  isolates it; watchdog restarts on failure; Slack alert on retry exhaustion; the
  parliamentary HLS sources are unaffected.
- **YouTube ToS exposure on archiving** - per-channel archive opt-in, short
  retention, private bucket, documented posture; operator decision per channel;
  embeds (not re-hosted streams) on the page.
- **LLM/STT cost while ON** - the toggle is the cost control; status surface makes
  "what is ON" obvious; retention bounds storage cost. A per-channel schedule
  (capture only during JT windows) is deliberate future work, not v1.
- **Stream/CDN URL rotation** - source re-resolved on every restart; `source_ref`
  stores the stable channel URL, never a resolved manifest.
- **Analyzer sessions are unbounded** - unlike video sessions; the hub must bound
  buffers (ring buffer, no per-session replay cache) and B's tests cover multi-hour
  session memory behaviour.
- **Backoffice epic overlap** - VER-206..208 fill `/backoffice` sections and touch
  the shared navigation in parallel with this epic. Mitigated structurally: D and E
  depend on VER-206, E adds its own section (the pattern VER-207/208 already use),
  and the section 5 API contract is owned entirely by this epic.
