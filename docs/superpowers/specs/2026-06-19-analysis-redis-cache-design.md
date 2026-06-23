# 24h Redis cache for video analysis + fact-check section cleanup

- Status: approved design (brainstorming complete)
- Date: 2026-06-19
- Owner epic: to be created in Linear (VER-xxx)

## Problem

Analysing an imported/uploaded video is expensive and fully ephemeral. Today the
live pipeline streams every result over the WebSocket and persists nothing
(migration `0008_drop_segment_results` removed the last result tables). Re-watching
the same video re-runs AssemblyAI transcription and the entire fact-check stack
(retrieval + LLM verification, including the default-off deepseek-v4-pro second pass
from VER-122) from scratch every time.

We want an already-fully-analysed imported video to reuse its transcript + verdicts
for 24h, then expire and re-analyse. Separately, the fact-check section should only
show claims with a concrete result.

## Goals

- Cache the complete analysis of a **finite** video (upload / YouTube import / sample)
  in Redis with a 24h TTL, keyed by `video.ID`.
- On a cache hit, deliver the full transcript + verdicts to the player **instantly**,
  with no AssemblyAI and no LLM calls.
- Keep the frontend experience identical: the same WebSocket frame types arrive, the
  active-subtitle highlight still follows playback time. Nothing about the player UI
  changes except where the data comes from.
- Tighten the fact-check section to show only claims with a concrete verdict
  (credible / disputed), excluding `unverifiable`/invérifiable.

## Non-goals

- No caching or replay for **live streams**. They are unbounded, one-shot, and the
  same URL may carry different content later; they always analyse fresh and write
  nothing.
- No resuming a partially-analysed video. A video that was not analysed to completion
  is re-analysed from scratch next time (see "Partial sessions").
- No change to the fact-check verdict pipeline itself (fast path, retrieval, verifier,
  or the VER-122 second pass). The cache wraps the pipeline; it does not alter verdicts.
- No change to the inline per-statement claim view. Unverifiable claims still appear
  there — only the dedicated fact-check section is tightened.

## Decisions (resolved during brainstorming)

1. **Filter semantics:** the fact-check section keeps only resolved claims with a
   concrete verdict. `status === "verified"` is already required today; we additionally
   exclude verdicts whose value is `unverifiable`/invérifiable.
2. **Scope:** imported/uploaded videos only (finite, replayable). Live streams excluded.
3. **Replay style:** instant full load — all cached frames are sent at session open and
   the stream closes. The existing active-subtitle highlight is driven by playback time,
   so it still works with everything loaded up front.
4. **Partial sessions:** cache only on completion. A session that ends before
   transcription reaches end-of-file writes nothing and is re-analysed next time.
5. **Infrastructure:** prod uses AWS ElastiCache provisioned via Terraform; local dev
   uses a Redis container in docker-compose.

## Architecture

The live pipeline is untouched. `LiveAnalyzer.Run()` returns a `<-chan LiveEvent`
that `internal/handler/live.go` serialises into WS frames (`subtitle`, `claims`,
`claim_result`, `result`, `speaker_tally`, `consistency`). We wrap that channel.

### Components

- **`AnalysisCache` store** (`internal/store`, new): a small interface with a Redis
  implementation and a no-op implementation used when caching is disabled or Redis is
  unreachable. Methods roughly:
  - `Get(ctx, videoID) (*AnalysisSnapshot, bool, error)`
  - `Put(ctx, videoID, snapshot) error` (sets the 24h TTL)
  The Redis impl uses `github.com/redis/go-redis/v9` (v9.20.x). Connection from
  `REDIS_URL` via `redis.ParseURL`; startup `Ping` for a liveness check that degrades
  to no-op (logged) rather than failing the server.

- **`AnalysisSnapshot`** (versioned, JSON-serialisable): the ordered list of
  `LiveEvent`s emitted during a completed session, plus a schema `Version` and the
  `VideoID`. Storing domain-level events (not pre-serialised wire frames) keeps the wire
  format single-sourced in the handler; replay re-uses the existing serialisation path.
  A schema-version mismatch on read is treated as a cache miss.

- **Capture tee** (in the live handler/session wiring): for finite videos, each
  `LiveEvent` forwarded to the client is also appended to an in-memory accumulator.

- **Completion signal:** the snapshot is persisted only when the audio source reaches
  EOF and all analysis units have flushed — a genuine completion. A client disconnect
  or context cancellation before EOF persists nothing. This requires distinguishing
  "audio exhausted, pipeline drained" from "client went away"; the handler already
  owns both the audio source lifecycle and the client connection, so the signal is
  derived there.

- **Replay path** (session open): before constructing any analyzer/transcriber, the
  handler calls `AnalysisCache.Get(videoID)` for finite videos. On a complete hit it
  writes every stored event through the same serialiser and closes the stream — no
  AssemblyAI, no LLM. On miss / disabled / Redis error it proceeds with normal live
  analysis (graceful degradation; the cache is never required for correctness).

### Cache key

`analysis:v1:{video.ID}`. `video.ID` is the canonical Postgres primary key; YouTube
re-imports dedupe to the same record, so the key is stable across re-imports. The `v1`
segment is the snapshot schema version and lets us invalidate the whole namespace by
bumping it.

### Data flow

```
session open
  └─ finite video? ── no ─> normal live analysis (no capture, no replay)
        │ yes
        ├─ AnalysisCache.Get(video.ID)
        │     ├─ complete hit ─> replay all events -> WS frames -> close   (no AssemblyAI/LLM)
        │     └─ miss/disabled/error
        │           └─ run LiveAnalyzer; tee events to client + accumulator
        │                 └─ on EOF completion ─> AnalysisCache.Put(video.ID, snapshot, TTL=24h)
        │                 └─ on early disconnect ─> discard accumulator
```

## Fact-check section cleanup (frontend)

In `stack/frontend/src/lib/live/fact-checks.ts`, `deriveFactChecks` already skips any
claim whose `status !== "verified"`. We add a verdict-value filter so the section keeps
only claims with a concrete result and drops `unverifiable`. The legacy curated
`kind: "match"` entries and the inline per-statement view (`LiveClaimList`) are
unchanged. Covered by Vitest in the existing fact-checks test file.

## Configuration

- `REDIS_URL` (string, secret-ish): when set, caching is enabled; when empty, the
  no-op store is used and behaviour is exactly as today.
- `ANALYSIS_CACHE_TTL` (duration, default `24h`).
- Local dev: a `redis` service in `docker-compose.yml`, `REDIS_URL` wired to the
  backend container, `.env.example` updated.
- Secrets come from env/config only; `REDIS_URL` is never logged.

## Infrastructure (Terraform, prod)

- Engine: **Valkey** on ElastiCache (AWS-recommended, OSS/BSD, cheaper, wire-compatible
  with the go-redis client — no code changes vs Redis OSS).
- Topology: a single small node (`cache.t4g.micro`) with no replica is sufficient for a
  24h ephemeral cache — predictable flat cost, and a node failure only causes cache
  misses (added latency), never data loss. Serverless is reserved for spiky load.
- Resources: `aws_elasticache_cluster` (single node) or `aws_elasticache_replication_group`
  if a replica is wanted later, plus `aws_elasticache_subnet_group` (private subnets) and
  an `aws_security_group` allowing inbound 6379 from the backend ECS task SG only.
- Region `eu-west-3`. Stacked on the existing prod terraform chain (shared
  `prod/main.tf`). `REDIS_URL` is injected into the backend service env/secret.
- CI runs validate-only; `terraform apply` stays human-gated.

## Testing

- **Backend unit:** `AnalysisCache` Redis impl against `github.com/alicebob/miniredis/v2`
  (v2.38.x, go-redis/v9 compatible) — round-trip Put/Get, TTL expiry via
  `s.FastForward`, schema-version mismatch -> miss, no-op store when disabled. Snapshot
  marshal/unmarshal round-trip. Completion-vs-disconnect gating (only complete sessions
  persist; finite-only guard). Replay emits the exact stored event sequence.
- **Backend e2e:** analyse a finite sample to completion, confirm a snapshot lands in
  Redis; re-open the same video and confirm the frames are served from cache with no
  transcriber/LLM invocation; confirm a live stream writes nothing.
- **Frontend:** Vitest in the fact-checks test file — unverifiable verdicts excluded
  from the section, concrete verdicts retained, inline view unaffected.
- Go suite green under `-race`; lint/format clean; ESLint clean.

## Epic breakdown (5 cards)

- **A — Backend Redis cache infra.** Add `go-redis/v9`; config (`REDIS_URL`,
  `ANALYSIS_CACHE_TTL`); `AnalysisCache` interface + Redis impl + no-op with graceful
  degradation and startup ping; docker-compose `redis` service and `.env.example`.
  Tests via miniredis.
- **B — Capture & persist completed snapshots.** Versioned `AnalysisSnapshot`; tee the
  `LiveEvent` stream for finite videos; persist on EOF completion only; discard on early
  disconnect; finite-only guard. Tests. *(depends on A)*
- **C — Cache-hit instant replay.** Check the cache at session open for finite videos;
  replay all stored events through the existing serialiser and close; skip the analyzer
  and transcriber entirely on a hit. Tests + e2e. *(depends on B)*
- **D — Fact-check section cleanup.** Exclude `unverifiable` verdicts from the
  fact-check section in `fact-checks.ts`; keep concrete credible/disputed; inline view
  untouched. Vitest. *(independent)*
- **E — Terraform ElastiCache (prod).** Provision Valkey (subnet group + SG + node),
  inject `REDIS_URL` into the backend service; stacked on the prod chain; CI
  validate-only; apply human-gated. *(depends on A's env contract; deliverable in parallel)*

Dependency order: A -> B -> C. D independent. E parallel to B/C, applied last.

## Risks & mitigations

- **Snapshot size / large transcripts.** A long video yields many events. Mitigation:
  store as a single compact JSON value; revisit chunking only if values approach Redis
  value-size limits in practice.
- **Stale verdicts after a corpus/model change.** A 24h TTL bounds staleness; the `v1`
  key namespace allows a global invalidation by bump if a breaking change ships.
- **Completion misdetection** persisting a partial snapshot. Mitigation: persist strictly
  on the EOF/drained signal, with a unit test for the disconnect-before-EOF path.
- **Redis outage.** No-op fallback + startup ping mean an outage degrades to today's
  behaviour (analyse fresh), never an error.

## References (verified 2026-06-19, Context7 + web)

- go-redis v9.20.1 — `github.com/redis/go-redis/v9`; `redis.ParseURL` + `Ping` health-check.
- miniredis v2.38.0 — `github.com/alicebob/miniredis/v2`; `RunT`, `FastForward`.
- ElastiCache Valkey is the AWS-recommended engine (OSS/BSD, cheaper, wire-compatible);
  single `cache.t4g.micro` node fits a 24h ephemeral cache; resources
  `aws_elasticache_cluster`/`replication_group` + `subnet_group` + `security_group`.
