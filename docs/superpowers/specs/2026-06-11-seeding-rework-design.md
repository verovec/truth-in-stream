# Seeding Rework: one command for a complete dev environment

Date: 2026-06-11
Status: Approved (pending written-spec review)

## Problem

Dev seeding grew apart from the application. Today it is split across two
unrelated paths:

- `cmd/seed` (run via the `seed` docker-compose service, `make seed`/`make reset`)
  loads three offline fixtures from a committed embedding cache: `claims.json`,
  `wiki_chunks.json`, `demo_results.json`. No API key or network required.
- `VideoService.EnsureSamples`, called only at **server startup**, seeds the
  curated sample video *records* (`defaultSampleVideos`). The media bytes those
  records point at are populated by separate ad-hoc tooling, and `SizeBytes` is
  left `0`.

Consequences:

- `make reset` reseeds claims/wiki/demo but **not** sample videos (they are
  startup-only), so the gallery is empty until the backend restarts
  (the `soft-reset-wipes-sample-video` defect).
- There is no single command that produces a fully populated dev environment.
- Sample media is not reproducible: records exist, bytes do not, `SizeBytes=0`.
- The demo fixture and the sample record describe the same clip but are not
  linked: demo results attach to a video id derived from a `source` string
  (`service.VideoID(source)`), while video records use a UUID `id`.

Note on "newer entities": uploads (migration 0006) and YouTube videos
(migration 0007) are created at **runtime** (operator-driven), not curated seed
data. The only legitimately seedable new entity is the curated **sample** video.
This rework brings samples into the unified seed path; it does not fabricate
fake uploads or YouTube rows.

## Goal

A single command, `cmd/seed` with no flags, produces a fully populated, current
dev environment — claims, wiki, demo results, **and** sample video records plus
their media bytes — without a backend restart, and without hard-failing when run
offline.

## Architecture

### Unified seed command

Add a fourth dataset, `-videos`, to `cmd/seed` alongside `-claims -wiki -demo`.
No flags continues to mean "seed everything", now including videos.

```
cmd/seed
  -claims   curated claims          (offline)
  -wiki     wikipedia subset        (offline)
  -demo     demo-video results      (offline)
  -videos   sample video records + media bytes   <- new
(no flags = seed everything)
```

The curated sample definition currently in `service.defaultSampleVideos` moves
to a single shared location so the seed command is the one source of truth for
what the samples are. (Exact home decided in the plan: either the `seed` package
or a small shared fixture; it must not introduce an HTTP dependency into the
seed/store layers per the layering rules.)

### Server startup no longer seeds samples

`VideoService.EnsureSamples` and its `cmd/server` call site are **removed**.
Samples come only from `cmd/seed`. The `seed` compose service already runs to
completion before `backend` starts (compose `depends_on`), so a normal
`make up`/`make reset` yields a populated gallery with no restart. This is the
core fix for the startup/reset split.

### Sample media: records + bytes

`-videos` runs in two parts:

1. **Record upsert (offline, always):** upsert the sample video record(s)
   idempotently via the existing `ON CONFLICT (object_key)` store path. Status
   `ready`, kind `sample`.
2. **Media bytes (online, best-effort):**
   - Fetch bytes from a pinned, configurable URL. Default: a widely-used stable
     test clip (Google-hosted BigBuckBunny mp4); override via `SAMPLE_VIDEO_URL`.
   - Cache the fetched bytes under a **gitignored local cache dir**
     (e.g. `seed/media-cache/`). Later reseeds reuse the cache (offline OK).
   - Upload the bytes to object storage (minio locally) at the sample's
     `object_key`.
   - Set `SizeBytes` from the real fetched file.

```
first reset (online):  download -> local cache -> upload -> SizeBytes set
later resets:          reuse cache (offline OK)
no network + no cache: seed record, log warning, skip bytes (reset succeeds)
SAMPLE_VIDEO_URL unset: same graceful skip (record only)
```

**Graceful skip:** if the host is unreachable and there is no cache, the run
still seeds the record and logs a clear warning rather than failing, so
`make reset` never hard-fails offline. The default "seed everything" stays
resilient.

Content caveat: the committed `demo_results.json` transcript is about
"Common Myths" fact-checking; a generic test clip will not match the overlay
content. This is acceptable for dev seeding and is documented; operators who
want a matching clip set `SAMPLE_VIDEO_URL`.

### Identity reconciliation

The seeded sample record and the seeded demo results should describe the same
clip. During implementation, confirm how `demo_results.json`'s `source` resolves
(`service.VideoID(source)`) relative to the sample's `object_key`, and either
align them so the demo plays against the sample, or document explicitly why they
remain separate. This is a clarifying step in the plan, not new schema.

## Makefile / compose

- `make reset` and the `seed` compose service now produce a populated gallery.
- Add `make seed-videos` mirroring `seed-claims`/`-wiki`/`-demo`.
- The media cache dir is gitignored and persisted across reseeds (a named volume
  or bind mount for the `seed` service).
- Document `SAMPLE_VIDEO_URL` where the other seed env is documented.

## Testing

No code without tests. All behaviour changes ship with tests in the same change.

- **Unit (table-driven, `internal/seed`):** the new video-seed loader/runner —
  record upsert idempotency; cache hit vs miss; graceful-skip on an unreachable
  host (record seeded, bytes skipped, warning); `SizeBytes` set from real bytes.
  Use a fake media store and a fake fetcher; **no real network in tests.**
- **Integration:** extend `seed_integration_test.go` to cover `-videos`
  (record present, bytes uploaded through a fake/minio media store).
- **Service:** update `video_test.go` to reflect the removal of `EnsureSamples`
  (delete its idempotency/error tests or relocate the shared-definition checks).
- `go test -race ./...` green; `gofmt`/`gofumpt`, `go vet`, `golangci-lint` clean.

## Out of scope (YAGNI)

- Seeding fake uploads or YouTube rows (runtime-only entities).
- Changing the embedding cache format or the offline claims/wiki/demo path.
- Any production or non-dev seeding. This is local-dev fixtures only.

## Acceptance criteria

1. `make reset` (or `cmd/seed` with no flags) populates claims, wiki, demo, and
   the sample video gallery, with no backend restart required.
2. Run offline with no media cache: seeding succeeds, sample records present,
   media bytes skipped with a clear warning (no hard failure).
3. Run online (or with a warm cache): sample media bytes are in object storage
   and `SizeBytes` reflects the real file.
4. `EnsureSamples` no longer runs at server startup; samples originate solely
   from `cmd/seed`.
5. New and updated tests pass under `-race`; lint and format clean.
6. A code review has been run and every correctness finding resolved.
