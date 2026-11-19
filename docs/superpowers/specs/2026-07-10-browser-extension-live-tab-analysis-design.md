# Browser Extension: Live Tab-Audio Fact-Checking - Design

Date: 2026-07-10
Status: Design only. No implementation, no store publication. Draft spec for review.

A Chrome + Firefox extension that captures the audio of the tab the user is watching
(YouTube live, a news site, a parliamentary portal) and streams it through the existing
truth-in-stream live fact-checking pipeline, showing subtitles, per-claim verdicts, and
speaker tallies in real time next to the page.

## Decisions

- Reuse the existing live wire contract unchanged: binary 16 kHz mono s16le PCM frames
  up, typed JSON frames (`interim`, `subtitle`, `claims`, `claim_result`, `consistency`,
  `speaker_tally`, `result`) down. No new analysis code.
- New backend route for ephemeral tab sessions (no `videos` record, no snapshot
  cache/persist), factored from the existing `/api/videos/{id}/live` handler.
- Auth: the extension runs its own OIDC Authorization Code + PKCE flow against Keycloak
  via `identity.launchWebAuthFlow` (both browsers) and appends `?access_token=` to the
  WS upgrade - the mechanism the backend already supports.
- Origin policy: token-authenticated WS upgrades skip the Origin allowlist (the token is
  an explicit, non-ambient credential, so the CSRF rationale for Origin checking does
  not apply). Cookie-less upgrades keep today's behaviour.
- Capture is asymmetric by browser and stays that way:
  - Chrome (MV3, minimum 116): `chrome.tabCapture.getMediaStreamId()` in the service
    worker + an offscreen document that materializes the stream, hosts the AudioWorklet,
    and owns the WebSocket. Captures the whole tab mix regardless of media CORS.
  - Firefox: no `tabCapture`, no `getDisplayMedia` audio, no offscreen documents. A
    content script taps the page's `<video>`/`<audio>` element
    (`MediaElementAudioSourceNode`), relays PCM to the background script over a runtime
    Port, and the background script owns the WebSocket. Cross-origin media without CORS
    headers yields silent zeros - detected and surfaced, not worked around.
- UI: Chrome side panel / Firefox sidebar showing the live feed (interim caption,
  statement list with claim verdict chips, speaker tally). No in-page overlay in v1.
- Phasing: Chrome first (robust capture path), Firefox second. DRM/EME content is out of
  scope permanently - it is uncapturable by any extension API in either browser.
- New global backend session cap (`LIVE_MAX_SESSIONS`) plus extension-side silence
  auto-stop, because the extension makes it trivial to open many concurrent AssemblyAI
  sessions.
- Shared code (PCM encode/resample, frame types) is vendored into the extension from
  `stack/frontend/src/lib/live/`, not extracted into a shared package yet (same deferral
  rule as the LLM adapter consolidation: extract when a third consumer appears).
- Distribution now: developer-mode unpacked (Chrome) and temporary add-on (Firefox) on
  the developer's machine only. Anything beyond that requires signed artifacts (unlisted
  AMO XPI, CWS draft) - explicitly deferred.

## 1. Goal and product fit

The web app fact-checks videos hosted by jeminforme.fr. The extension inverts the
direction: instead of bringing the video to the platform, it brings the platform to
whatever page the user is already watching. It complements the TV-channels epic
(server-side capture of official lives): the extension covers the long tail the server
fleet will never enumerate, with the user's own browser doing the capture.

Success criteria for v1:

- On a Chrome tab playing a French political stream, clicking the extension action
  starts analysis within ~2 s; subtitles and claim verdicts appear in the side panel as
  they do on the web live page.
- The tab's audio keeps playing to the user during capture.
- Closing the panel, stopping, or closing the tab tears the session down cleanly (no
  orphan AssemblyAI sessions).
- The same core works on Firefox for CORS-clean media, with an explicit "this site's
  media cannot be captured" state otherwise.

## 2. What is reused (the existing wire contract)

The live pipeline's client contract, mapped from the code:

- Endpoint today: `GET /api/videos/{id}/live` (`internal/handler/handler.go:47`,
  `internal/handler/live.go:253`). Auth is `middleware.RequireIdentity` on the whole
  `/api/` subtree; on a WebSocket upgrade the token may arrive as `?access_token=`
  (`internal/middleware/identity.go:121-141`). Any verified token connects; the admin
  role only unlocks debug evidence detail.
- Audio up: raw binary WS frames, PCM s16le mono 16 kHz (`stack/frontend/src/lib/live/
  pcm.ts`). No client -> server control frames; non-binary frames are ignored. The
  backend coalesces inbound frames to 100 ms before AssemblyAI (50-1000 ms frame
  requirement, `internal/transcribe/assemblyai.go:277-326`), so client frame size is
  free; read limit is 1 MiB.
- Down: JSON text frames discriminated by `type` (`internal/handler/live.go:459-532`).
  The verify path resolves a claim-bearing statement through `claims` +
  `claim_result` frames only; a statement-level `result` appears only for
  non-claim outcomes (skip, not-a-claim, unit error).
- Backpressure: the web client drops audio frames when `bufferedAmount >= 64 KiB`
  (`socket.ts:10`); the extension keeps the same rule.

The extension reuses all of this verbatim. Vendored from the frontend: `pcm.ts`
(resample + s16le encode), `pcm-capture-processor.js` (pass-through worklet), and the
frame type definitions / reducer shape of `use-live-analysis.ts` (ported off React
hooks).

## 3. Backend changes

### 3.1 Ephemeral session route

`GET /api/live/session` (name final at implementation): same WS loop, transcriber
relay, verify-path dispatch, ping/keepalive, and frame serialization as
`/api/videos/{id}/live`, but:

- no `videos` record lookup, no replay-on-open, no snapshot persist on completion
  (recorder/replayer are nil for this route);
- a server-generated session id used only for logging/metrics.

Implementation shape: factor the body of `liveHandler` into a shared
`runLiveSession(conn, analyzer, opts)`; the two routes become thin wrappers. No
change to the analysis stack.

### 3.2 Origin policy for token-authenticated upgrades

Today `websocket.Accept` enforces `OriginPatterns` derived from the single
`CORS_ALLOWED_ORIGIN` (nil in prod = same-origin only), which rejects any extension
origin. A static allowlist cannot work for extensions:

- Firefox extension-page origins are `moz-extension://<uuid>` with a per-install random
  UUID - unknowable in advance.
- Firefox content scripts inherit the page's origin (youtube.com, ...), which is
  arbitrary.

The Origin check is a CSRF defence for ambient (cookie) credentials. A WS upgrade
authenticated by an explicit `?access_token=` carries no ambient credential, so the
check adds nothing there. Rule: if the upgrade authenticated via the `access_token`
query parameter (or Bearer header) with a verified token, accept regardless of Origin
(`InsecureSkipVerify` on that branch, with a comment stating the CSRF rationale);
otherwise keep today's behaviour unchanged. Table-driven tests cover all four
combinations (token x origin).

### 3.3 Session caps

There is currently no global bound: N clients = N AssemblyAI realtime sessions = N
verify worker pools. The extension lowers the barrier to opening sessions, so v1 adds:

- `LIVE_MAX_SESSIONS` (global semaphore, default generous, 503 + JSON reason when
  exhausted, frontend/extension show "capacity reached, retry");
- per-identity cap of 1 concurrent extension session (a second connect from the same
  subject closes the older one).

Both apply to the new route; retrofitting the video route can ride the same change.

### 3.4 Pre-existing gap surfaced by this design

The web frontend's own `liveSocketUrl` never appends `?access_token=` and the httpOnly
cookie it does send is never read by `RequireIdentity` - a direct-to-backend WS in prod
has no working credential path from the web app today (known VER-147 follow-up). The
extension work makes `?access_token=` the exercised, tested path; fixing the web app's
`liveSocketUrl` should be a sibling card in the same epic.

## 4. Auth design

- Keycloak gets one new public client (`extension`) with PKCE required and two redirect
  URIs: `https://<chrome-extension-id>.chromiumapp.org/` and Firefox's
  `identity.getRedirectURL()` host. Realm config lives with the existing realm
  provisioning; dev realm gets it too.
- Extension IDs must be pinned so redirect URIs and any future origin config stay
  stable: `key` in the Chrome manifest (fixes the ID for unpacked dev installs),
  `browser_specific_settings.gecko.id` in Firefox (also makes `getRedirectURL()`
  deterministic).
- Flow: side panel "Sign in" -> `identity.launchWebAuthFlow` (Authorization Code +
  PKCE) -> tokens kept in `storage.session` (access) and `storage.local` (refresh,
  encrypted at rest by the browser profile). Silent refresh before expiry; the WS is
  authenticated only at upgrade time, so mid-session expiry does not drop the
  connection - reconnects fetch a fresh token first.
- Explicitly rejected alternative: reading the web app's `kc_access` cookie via
  `browser.cookies` (needs broad host permission, couples to cookie internals, breaks
  when the user is not logged into the web app).

## 5. Extension architecture

New workspace directory `stack/extension` (own package.json, TypeScript, Vitest; the
cross-browser build tool - WXT is the current leading candidate - gets a `/research`
pass at implementation time per workspace rules).

### 5.1 Chrome (MV3, `minimum_chrome_version: "116"`)

Components and flow:

1. Action click (user gesture) -> service worker calls
   `chrome.tabCapture.getMediaStreamId({targetTabId})`, ensures the offscreen document
   exists (`reason: USER_MEDIA`), passes it the stream id + a fresh access token, opens
   the side panel.
2. Offscreen document: `getUserMedia({audio: {mandatory: {chromeMediaSource: 'tab',
   chromeMediaSourceId}}})` -> `MediaStreamSource -> AudioWorkletNode (pass-through) ->
   destination`. The pass-through to `destination` is load-bearing: tab capture mutes
   the tab's own playback otherwise. Worklet blocks are resampled to 16 kHz s16le on
   the offscreen main thread (vendored `pcm.ts`), coalesced to ~100 ms buffers to keep
   WS message rate sane over WAN (the web app's ~3 ms frames are fine on localhost,
   wasteful here), and sent on the WebSocket.
3. The WebSocket lives in the offscreen document, not the service worker. The offscreen
   document is not subject to the SW 30 s idle kill, the audio producer is already
   there, and no keepalive timer gymnastics are needed. The SW stays a thin controller
   (start/stop, badge, session state in `storage.session`).
4. Inbound JSON frames are relayed via `runtime` messaging to the side panel, which
   runs the ported statement/claims reducer and renders the feed.

Manifest permissions: `tabCapture`, `offscreen`, `sidePanel`, `identity`, `storage`,
plus the backend origin as a host permission. No content scripts on Chrome.

### 5.2 Firefox

No `tabCapture`, no `getDisplayMedia({audio})` (returns zero audio tracks on every OS),
no offscreen documents. The only viable path:

1. Action click -> background script (event page) injects the capture content script
   into the active tab (`scripting.executeScript`, `activeTab`).
2. Content script locates the dominant playing media element (largest playing
   `<video>`, else `<audio>`; `MutationObserver` re-attaches across SPA element swaps),
   builds the same graph the web app uses (`MediaElementAudioSourceNode ->
   worklet -> destination` - this is exactly `audio-capture.ts`, vendored). Element
   capture does not mute playback, so pass-through is belt-and-braces here. The worklet
   file ships as a static extension file declared in `web_accessible_resources` and is
   loaded via `runtime.getURL()` (never a blob URL - MV3 CSP forbids it).
3. PCM (resampled + coalesced in the content script) flows over a `runtime.connect`
   Port to the background script, which owns the WebSocket. The WS deliberately does
   not live in the content script: page CSP can interfere with page-context sockets,
   and the background survives page navigation.
4. Sidebar (`sidebar_action`) renders the same feed component as Chrome's side panel.

CORS reality: cross-origin media served without `Access-Control-Allow-Origin` produces
a silently muted capture (all-zero samples, no exception - spec-mandated). The content
script runs a zero-detector (N seconds of pure zeros while the element is playing and
unmuted) and the UI states "this site's media cannot be captured on Firefox" rather
than showing an empty feed. Sites inside cross-origin iframes need `all_frames`
injection and are best-effort.

### 5.3 Session lifecycle

- Start: action click (satisfies Chrome's user-gesture requirement for tabCapture).
- Stop: explicit stop button, tab close, capture stream ending, or silence auto-stop
  (no non-zero audio for a configurable window, default 3 min) - protects AssemblyAI
  hours when the user forgets a session on a music tab.
- Reconnect: exponential backoff capped at 8 s (same policy as the web hook), fresh
  access token per attempt, audio buffered up to the 64 KiB backpressure cap and
  otherwise dropped (live analysis tolerates gaps; it does not tolerate lag).
- Navigation: Chrome tab capture is documented to survive in-tab navigation (verify
  empirically early - flagged as an assumption); Firefox's content-script graph dies
  with the page and is re-injected on `webNavigation.onCompleted` while the session is
  active.

## 6. UI

Side panel / sidebar, French-first with the EN toggle, mirroring the web live page's
information design: connection/session state header, interim caption line, scrolling
statement list with per-claim verdict chips (credible / disputed / unverifiable +
manipulation flags), speaker tally strip. Sign-in state and capture-error states
(DRM/CORS/no-media) are first-class screens, not toasts. No in-page overlay in v1 -
it is the most fragile surface (site CSS collisions, fullscreen video layers) and adds
nothing analytical.

## 7. Testing

- Vendored PCM chain and frame reducer: Vitest unit tests in `stack/extension`
  (ported from the frontend's existing tests where they exist).
- Backend: table-driven Go tests for the new route (no-record session, nil
  recorder/replayer), the token-vs-origin acceptance matrix, `LIVE_MAX_SESSIONS`
  exhaustion, and per-identity displacement. `go test -race ./...`.
- E2E (Chrome, CI): Playwright launches Chromium with the built extension, a fixture
  page plays a bundled French audio clip, a stub backend WS asserts 16 kHz PCM frames
  arrive and replays canned `claims`/`claim_result` frames, the test asserts the side
  panel renders them. This exercises the real entrypoint (built extension), per the
  e2e-real-entrypoint rule.
- Firefox: `web-ext run`-driven smoke where CI allows; otherwise a written manual
  checklist (CORS-clean site, CORS-tainted site, DRM site) attached to the card.

## 8. Epic breakdown (cards to create at implementation time)

- A. Backend: ephemeral live-session route + token-authenticated origin policy +
  `LIVE_MAX_SESSIONS`/per-identity caps, tests. Also fix the web app's
  `liveSocketUrl` to append `?access_token=` (closes the VER-147 follow-up).
- B. Keycloak `extension` client (dev + prod realm config) + extension OIDC PKCE
  module + token storage/refresh.
- C. Chrome extension: scaffold, service worker + offscreen capture pipeline,
  side panel feed UI, e2e harness.
- D. Firefox: content-script element capture + background WS relay + sidebar +
  CORS-silence detection.
- E. Hardening: silence auto-stop, reconnect/navigation resilience, i18n polish,
  manual Firefox checklist, docs.

A -> B -> C are sequential; D and E follow C.

## 9. Gotchas and risks (the honest list)

1. Firefox capture is structurally weaker. No tab capture exists (Bugzilla 1391223 is
   P5/unassigned); element capture silently yields zeros for cross-origin media without
   CORS headers, which includes many third-party players. Firefox support is therefore
   "works where the site cooperates", and the UI must say so. This is a browser
   limitation, not an implementation bug to fix later.
2. DRM/EME content is uncapturable everywhere (tabCapture, getDisplayMedia,
   captureStream all refuse when a CDM is active). TF1+/M6+/Netflix-class sources are
   permanently out - consistent with the TV-channels epic's exclusions. The UI needs a
   clear "DRM-protected, cannot analyse" state (detectable via the zero-detector plus
   capture-start failure).
3. Chrome tab capture mutes the tab unless the captured stream is routed back to an
   AudioContext destination in the offscreen document. If the offscreen document
   crashes mid-session the user's tab goes silent until the stream closes - teardown
   paths must always close the stream.
4. MV3 service worker lifetime (30 s idle kill) makes the SW the wrong home for the
   socket or any audio state. Everything long-lived sits in the offscreen document
   (Chrome) or event-page background (Firefox).
5. Origin allowlisting cannot authenticate extensions (per-install moz-extension UUIDs,
   page-origin content scripts). The design shifts that route to explicit token auth
   and skips Origin there; reviewers should scrutinise that CSRF argument.
6. The existing route contract assumes a stored video (snapshot replay/persist).
   Ephemeral sessions need the new route; nothing may accidentally persist tab audio
   or transcripts (privacy posture: audio is relayed to AssemblyAI and discarded, same
   as live today - but now the audio comes from arbitrary third-party pages, so this
   guarantee must be stated in the extension's privacy copy at publication time).
7. Cost exposure: each session is a paid AssemblyAI realtime session plus LLM verify
   calls, and the extension makes opening sessions effortless. Mitigations are the
   global/per-identity caps and silence auto-stop; without them a handful of forgotten
   tabs burns real money.
8. Tab audio is the whole tab mix on Chrome: notification pings, autoplaying sidebar
   ads, and background players all land in the transcript. Diarization plus the
   precheck/not-a-claim path absorbs most of it, but expect noisier input than the
   curated web pipeline.
9. Auth from an extension is its own subsystem: httpOnly cookies are unreachable by
   design, so PKCE via launchWebAuthFlow, pinned extension IDs (Chrome `key`, Firefox
   `gecko.id`), Keycloak redirect-URI registration, and refresh handling are all
   mandatory before the first byte of audio flows. Long sessions outliving the access
   token are fine (auth is at upgrade only) but every reconnect needs a fresh token.
10. The web app's own live WS has no working direct-to-backend credential today
    (`liveSocketUrl` sends no token; the httpOnly cookie is never read). The extension
    epic must not silently depend on dev-only behaviour - card A fixes the web path
    alongside.
11. Chrome's "capture survives navigation" is documented but weakly corroborated;
    verify empirically in week one. Firefox's path definitively dies on navigation and
    needs re-injection.
12. AudioWorklet loading from a content script (Firefox path) is a known rough edge:
    must be a static packaged file via `web_accessible_resources` + `runtime.getURL()`;
    blob URLs violate MV3 CSP. Budget for flakiness here.
13. Distribution constraints bite even for private testing beyond one machine: Firefox
    requires AMO-signed XPIs even self-distributed (temporary add-ons vanish on
    restart; unsigned loading only on Dev Edition/Nightly/ESR), and Chrome now
    auto-disables developer-mode unpacked extensions after updates. "Not publishing"
    still means signing artifacts the moment a second person tests it.
14. Chrome Web Store policy (a new privacy-disclosure regime lands 2026-08-01) treats
    tabCapture as a high-scrutiny permission; when publication eventually happens,
    the privacy justification and data-flow disclosure (audio -> backend -> AssemblyAI)
    must be ready.
15. Duplicate analysis: a user with the web app live page and the extension both
    running pays for two sessions of the same content. Not blocked in v1; the
    per-identity cap bounds it at web+1.

## 10. Out of scope for v1

- In-page overlay rendering; Safari; mobile browsers.
- Any server-side recording/persistence of tab sessions.
- Store publication (CWS/AMO listing, signing pipeline) - explicitly deferred; only
  developer-machine installs for now.
- Capturing OS/system audio or non-page sources.
- Client-side transcription or analysis fallbacks for DRM/CORS-blocked media.
