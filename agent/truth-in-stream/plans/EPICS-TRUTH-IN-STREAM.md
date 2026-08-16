# EPICS - TRUTH-IN-STREAM

Epic groupings of the `Todo` cards, sized so one agent owns one epic end to end.
This is the handoff map: give each epic below to a separate agent. Read the
**Cross-epic dependencies** and **Shared-file conflict rules** sections before running
two epics in parallel - they are what keep the branches from colliding at merge.

Source of truth for card ordering stays `ROADMAP-TRUTH-IN-STREAM.md` (the per-card ready
queue). This file is the coarser epic-level view. Keep both in sync when cards change.

On the Linear board each epic is a label (`epic:ingestion`, `epic:backoffice`, `epic:tv`,
`epic:preanalysis`) applied to all its cards - group-by-label to see the swimlanes. Labels
are metadata only and do not affect `/pick` or the ready queue.

Status 2026-07-17: Epics A, B, C, and D are fully delivered (all cards Done). Epic D
(VER-216..222) merged to `dev` via PRs #251-#255, #258, #259 (+ deflake #256) on
2026-07-17; docs in `docs/video-preanalysis.md`.

Status 2026-08-16: Epic E delivered 2026-07-21 (PRs #261-#263). Epic F (vector-first
verdicts, VER-224..230) created in Linear; the same day an epic run delivered
VER-224 (PR #268), VER-226 (PR #269), VER-227 (PR #270), and VER-229 (PRs #271+#272)
to `dev`, all Done. VER-225 needs a local-inference training environment (Docker,
seeded ClaimReview corpus) and stays `Todo`; VER-228 and VER-230 wait behind it.

---

## Epic A - Ingestion robustness & source expansion (VER-188..204, 17 cards)

**Scope for its agent.** Queue/worker resilience, resumable crawls, the source-connector
framework and its connectors, hybrid retrieval + the eval gate + the DeepSeek terminal gate,
and an ingestion docs refresh. Backend-internal + Terraform + `docs/` only. No frontend.

**Context.** Hardens the existing ingestion pipeline (no AMQP reconnect, no DLQ, non-resumable
crawls today) and broadens the evidence/claim corpora, then adds French lexical + RRF hybrid
retrieval behind a benchmark gate and a last-resort reasoner that returns "unverifiable" below
0.90 confidence. Spec: `docs/superpowers/specs/2026-07-10-ingestion-resilience-sources-search-design.md`.

**Cards & internal order** (each `->` = left must be `Done`/`In Review` before right starts):
- Resilience: `VER-188 -> VER-193 -> VER-196`; `VER-189`; `VER-190` (Terraform, independent).
- Sources: `VER-189 -> VER-194 -> {VER-197, VER-198, VER-199, VER-201}`; `VER-200` needs `VER-189 + VER-194`.
- Search: `VER-191 -> VER-195 -> VER-202 -> VER-203`; `VER-192 -> VER-202`.
- Docs close-out: `VER-204` needs `VER-196 + VER-200 + VER-203`.

**Entry cards (no deps, start immediately):** VER-188, VER-189, VER-190, VER-191, VER-192.

**Owns / touches:** `stack/backend/internal/{queue,worker,connector,store,retrieval,...}`,
`stack/terraform/*` (alarms/metrics lambda), `docs/` (ingestion pages). **Must NOT touch**
`docs/first-setup.md` (owned elsewhere). Minimal-to-no contact with `internal/handler/handler.go`.

**Parallelism:** starts now, runs fully parallel to B and C.

---

## Epic B - Backoffice admin section (VER-205..209, 5 cards)

**Scope for its agent.** An admin-only `/backoffice` route section; move video + PDF ingestion
behind it; add an admin video delete. Backend route gates + frontend shell/nav.

**Context.** "Ingestion is a backoffice operation; consumption stays for all authenticated
users." No Keycloak change needed - `realm.json` already seeds the `admin` role. `/backoffice`
must NOT be added to proxy `PUBLIC_PATHS`. Spec:
`docs/superpowers/specs/2026-07-10-backoffice-admin-video-ingestion-design.md`.

**Cards & internal order:**
- `VER-205` (backend: `RequireAdmin` on video POST routes + new `DELETE /api/videos/{id}`).
- `VER-206` (frontend `/backoffice` foundation: non-admin redirect, nav entry, `AppHeader` role prop).
- `VER-207` needs `205 + 206`; `VER-208` needs `207`; `VER-209` needs `205 + 208`.

**Entry cards (no deps, start immediately):** VER-205, VER-206.
**Deliver VER-205 and VER-206 first** - Epic C's UI cards are blocked on VER-206.

**Owns / touches:** `stack/backend/internal/handler/handler.go` (video routes),
`stack/frontend/src/components/app/app-header.tsx`,
`stack/frontend/src/app/app/_components/{app-shell,library-experience}.tsx`,
`stack/frontend/src/app/documents/_components/document-uploader.tsx`, new `/backoffice` route.

**Parallelism:** starts now, runs parallel to A. Owns the frontend nav shell that C extends.

---

## Epic C - Live TV channels (VER-210..215, 6 cards)

**Scope for its agent.** A TV channel registry + admin API, a live capture hub with per-channel
analyzer sessions, a headless `tvcapture` worker (streamlink -> ffmpeg -> S3 segments), a `/tv`
page, a backoffice TV management section, and its capture infra + runbook.

**Context.** Sources are official 24/7 YouTube lives + parliamentary portals; DRM'd broadcasters
(TF1+/france.tv/M6+) are permanently out of scope. Archive is per-channel opt-in, short retention.
The management UI mounts on the `/backoffice` shell from Epic B. Spec:
`docs/superpowers/specs/2026-07-10-tv-live-channels-design.md`.

**Cards & internal order:**
- `VER-210` (registry) `-> VER-211` (live hub) `-> VER-212` (worker) `-> VER-215` (infra/docs).
- `VER-213` (/tv page) needs `210 + 211 + `**`206 (Epic B)`**.
- `VER-214` (backoffice TV section) needs `210 + `**`206 (Epic B)`**.

**Entry card (no deps, start immediately):** VER-210. Then VER-211, VER-212 proceed on backend.
The two UI cards (VER-213, VER-214) wait for Epic B's VER-206.

**Owns / touches:** new `tv_channels` table + `internal/tv` store/service, `internal/handler/handler.go`
(tv routes), `stack/frontend/src/app/tv/*`, the shared nav + `/backoffice` shell (via 213/214),
`stack/terraform/*` (capture host, VER-215).

**Parallelism:** starts now on backend (210/211/212); UI cards gated on Epic B; VER-215 after 212.

---

## Epic D - Imported-video pre-analysis (VER-216..222, 7 cards)

**Scope for its agent.** One-shot server-side pre-analysis of imported videos persisted in
Postgres, analysed playback with pre-generated subtitles and a verdict-colored claim
timeline, backoffice analyse/re-analyse controls, and the docs close-out.

**Context.** Makes the 24 h Redis replay snapshot durable behind the same
`AnalysisRecorder`/`AnalysisReplayer` seam, adds the first server-side audio path
(ffmpeg from object storage into the existing AssemblyAI + verify pipeline, realtime
pacing), and mirrors the documents feature's `analysis_status` job lifecycle. Live TV and
non-analysed videos keep the live pipeline untouched; analysed videos never re-analyse
live. The ffmpeg/pacing piece (D2) and the claim-timeline UI (D5) are split out from
their neighbors because they are the epic's two genuinely unprecedented pieces (no
server-side audio path exists today; no custom seek-bar UI exists today) - the split is
for focused review, not merge-conflict avoidance (the chain is serial regardless). Spec:
`docs/superpowers/specs/2026-07-17-video-preanalysis-design.md`.

**Cards & internal order:**
- `D1 VER-216 (storage + read API) -> D2 VER-217 (ffmpeg audio-extraction adapter) ->
  D3 VER-218 (headless job + analyse endpoint) -> D4 VER-219 (player: REST hydration +
  button) -> D5 VER-221 (claim timeline strip)`.
- `D3 VER-218 -> D6 VER-220 (backoffice controls)`, running parallel to `D4`/`D5`
  (file-disjoint).
- `D7 VER-222 (docs + e2e close-out)` depends on `D5 + D6`.

**Entry cards (no deps, start immediately):** D1 VER-216.

**Owns / touches:** `stack/backend/migrations/0019_*`, `stack/backend/queries/`,
`stack/backend/internal/{store,service,handler,config}`, new
`stack/backend/internal/audioextract`, `stack/backend/cmd/server/main.go`,
`docker-compose.yml` (env forwarding), `stack/frontend/src/app/app/_components/*`,
`stack/frontend/src/components/playback/*`, `stack/frontend/src/lib/live/*` (REST
hydration), `stack/frontend/src/lib/video/api.ts`,
`stack/frontend/src/app/backoffice/_components/backoffice-video*`, `docs/`.

**Cross-epic dependencies:** none - Epics A, B, and C are delivered. If a future epic
runs beside D, the backend route registry `stack/backend/internal/handler/handler.go`
and `docker-compose.yml` stay append-only with a rebase rule (same doctrine as the B x C
hot files below).

**Parallelism:** can start as soon as its cards exist in Linear; no other epic is
currently open. Within the epic: D1 -> D2 -> D3 serial, then D4 -> D5 beside D6 (player
vs backoffice files), D7 last.

---

## Epic E - Claim detection quality (E1..E3, 3 cards, Linear ids pending re-auth) - DELIVERED 2026-07-21 (PRs #261-#263 to dev)

**Scope for its agent.** Tighten the verify-path detection loop: no-source claims always land
unverifiable with French-only rationales, the decomposer reads the full previous sentence group
as context and quotes only the claim core, and the live view merges a claim-bearing unit into
one statement with claim-core-only highlights.

**Context.** All three cards reshape the retrieve-then-verify path delivered by VER-83..92 and
refined by the claims-window work (PR #246). The knowledge fallback (`FACTCHECK_KNOWLEDGE_FALLBACK`)
is retired: a verdict may only be credible/disputed when at least one validated citation backs it.
The claims frame gains an additive `segment_ids` field so the frontend can merge a unit's member
statements. Linear was unreachable at authoring time (API key 401); the full card bodies live in
`LINEAR-LEDGER-CLAIM-QUALITY.md` beside this file for replay, label `epic:claim-quality`.

**Cards & internal order:**
- `E1 (no-source unverifiable + French rationales) -> E2 (previous-group context + minimal
  quotes + segment_ids) -> E3 (frontend merged statements + claim-core highlights)`.
- The chain is serial: E1 and E2 both edit `internal/service/verify_path.go`; E3 consumes E2's
  wire field.

**Entry cards (no deps, start immediately):** E1.

**Owns / touches:** `stack/backend/internal/verify/`, `stack/backend/internal/service/{verify_path,
political_path,live,document_analyzer,claimspan,video_analyzer}.go`, `stack/backend/internal/claimdecomp/`,
`stack/backend/internal/config/config.go`, `stack/backend/cmd/server/main.go`,
`stack/backend/internal/handler/live.go`, `stack/frontend/src/lib/live/*`,
`stack/frontend/src/app/app/_components/live-statement-list.tsx`, `docs/`.

**Cross-epic dependencies:** none - Epics A..D are delivered.

**Parallelism:** the epic itself can run now; internally strictly serial (E1 -> E2 -> E3,
delivered merge-then-next on `dev`).

---

## Epic F - Vector-first verdicts (VER-224..230, 7 cards)

**Scope for its agent.** Make generative LLM calls the last resort of the fact-check
pipeline: a French deterministic gate, a local encoder check-worthiness classifier with a
calibrated grey zone, an NLI stance scorer ahead of the LLM verifier, cross-encoder
reranking after hybrid fusion, evidence publication dates, per-claim telemetry, and a
final default flip with an enforced generative-call budget.

**Context.** The retrieve-then-verify path (VER-83..92) and hybrid RRF retrieval
(VER-191/195) match current best practice, but every check-worthiness and verdict
decision is still a Haiku call and the free heuristic gate is English-only, which inverts
the product doctrine of vector data first, generative models last. The epic inserts
non-generative stages in front of every LLM call - fine-tuned CamemBERTa-v2 classifier,
`camembertav2-base-xnli` NLI scorer (FEVER-style), Voyage `rerank-2.5` reranking - and
calibrates the grey zones from real telemetry. All schema changes are additive; no data
export/import is needed. Card bodies live in Linear (VER-224..230). The Linear label
`epic:vector-first` still needs one-click creation inside the `epic` label group (the MCP
connector cannot create labels); apply it to all seven cards once created.

**Cards & internal order:**
- `VER-225 (local classifier + inference substrate) -> VER-228 (NLI stance scorer)`.
- `VER-227 (published_at + recency) -> VER-229 (telemetry table)` - serialized only to
  keep migration numbering linear; no functional coupling.
- `VER-224` (French deterministic gate) and `VER-226` (reranker) are independent.
- `{VER-228, VER-226} -> VER-230` (vector-first defaults + LLM-call budget), last.

**Entry cards (no deps, start immediately):** VER-224, VER-225, VER-226, VER-227.

**Owns / touches:** `stack/backend/internal/service/{precheck_classifier,precheck_cascade,
precheck,verify_path,match,confidence}.go`, `stack/backend/internal/checkworthy/`, new
local-inference, NLI, and rerank packages under `stack/backend/internal/`,
`stack/backend/internal/eval/`, `stack/backend/migrations/` (additive, 0021+),
`stack/backend/queries/`, source renderers under `stack/backend/internal/source/` and
`stack/backend/internal/wiki/`, `stack/backend/internal/config/config.go`,
`stack/backend/cmd/server/main.go`, `docker-compose.yml`, `.env.example`, `docs/`.

**Cross-epic dependencies:** none - Epics A..E are delivered. VER-223 (transcript flow,
In Review) is frontend-only; no file overlap with this epic.

**Parallelism:** the four entry cards run in parallel under these hot-file rules:
`internal/config/config.go` and `cmd/server/main.go` are append-only (nearly every card
adds flags/wiring; second-to-merge rebases, never rewrites); the committed eval baseline
(`internal/eval/` fixtures) is extended by VER-225/226/228/230 with additive keys only;
`verify_path.go` is contended by VER-227 (fast-borrow age guard) and VER-228 (NLI
insertion) - VER-227 merges first or the later branch rebases. One agent owns the whole
epic; the ordering above is intra-epic scheduling, not a license to split it.

---

## Cross-epic dependencies

Only two hard cross-epic blocks exist, both from B into C:

```
B: VER-206 (backoffice foundation)  ->  C: VER-213 (/tv page nav entry)
B: VER-206 (backoffice foundation)  ->  C: VER-214 (backoffice TV section)
```

Consequence: Epic C can start immediately but its two frontend cards do not begin until Epic B's
VER-206 reaches `In Review`/`Done`. Everything else in C runs on the backend meanwhile.

Soft link (not a blocker, but a rebase warning):

```
B: VER-205  <-related->  C: VER-210    both edit the backend route table (see hot files below)
```

Epic A has no cross-epic dependency; it runs fully independent.

## Shared-file conflict rules (how to avoid merge collisions)

| Shared file | Contended by | Rule |
|-------------|--------------|------|
| `stack/backend/internal/handler/handler.go` (single route registry) | B (VER-205) and C (VER-210/211) | Append-only route additions. Do not merge VER-205 and VER-210/211 in the same instant without a rebase; whoever merges second rebases and re-adds its route lines. Epic A stays out of this file. |
| `src/components/app/app-header.tsx` + `src/app/app/_components/app-shell.tsx` (nav shell) | B (VER-206) owns; C (VER-213/214) extends | Already serialized by the VER-206 -> 213/214 block. Safe as long as C's UI cards wait for VER-206. |
| `docs/` | A (VER-204) and B (VER-209) | Different pages; no overlap. A must NOT touch `docs/first-setup.md`. |
| `stack/terraform/*` | A (VER-190) and C (VER-215) | Different resources, same dir. Rebase if both land close together. |

**Golden rules**
1. One epic = one agent. Never hand two agents cards from the same epic.
2. A, B, C are file-disjoint except through the links above; run all three in parallel.
3. Respect the B->C block: C's VER-213/214 do not start until B's VER-206 is `In Review`/`Done`.
4. The backend route file `handler.go` is the one genuine parallel-edit hot spot (B x C) - keep it append-only and rebase, never rewrite.

## Recommended agent assignment

| Agent | Epic | Start | Notes |
|-------|------|-------|-------|
| 1 | A - Ingestion robustness | now | Largest (17 cards, chained); longest runway, start first. |
| 2 | B - Backoffice | now | Land VER-205 + VER-206 first to unblock Epic C. |
| 3 | C - Live TV channels | now | Backend cards (210/211/212) now; UI cards (213/214) wait on B/VER-206. |
