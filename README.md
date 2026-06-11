# truth-in-stream

Real-time fact-checking for live streams.

## Stack

| Layer | Tech | Location |
|-------|------|----------|
| Frontend | Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) | `stack/frontend` |
| Backend | Go (standard-library `net/http` service) | `stack/backend` |
| Data | Postgres 16 + `pgvector` (vector store), Voyage AI `voyage-4` embeddings, ElevenLabs Scribe v2 transcription | `stack/backend` |
| Infra | Terraform on AWS, region `eu-west-3` | `stack/terraform` |

## Quick start

The local dataset (curated claims, a Wikipedia evidence subset, and the demo-video results)
seeds fully offline from a committed embedding cache, so **no API keys are needed to bring the
stack up and play the demo**. Set the operator login, then start everything with one command:

```bash
cp .env.example .env
# set AUTH_EMAIL / AUTH_PASSWORD_HASH / SESSION_SECRET (see Configuration);
# TRANSCRIPTION_API_KEY and EMBEDDING_API_KEY can stay empty for the demo.
make up
# frontend -> http://localhost:3000
# backend  -> http://localhost:8080/healthz
```

`make up` (`docker compose up -d --build`) runs, in order: Postgres, a one-shot `migrate`, a
one-shot `seed` that loads the claims, the Wikipedia subset, and the precomputed demo results
into pgvector, then the backend and frontend. Open http://localhost:3000, sign in, and the
bundled demo clip plays with the fact-check panel already populated from the seeded results -
no transcription or embedding API call. See [Local development data](#local-development-data)
to reset or reseed, and [Demo](#demo) for the demo itself.

## Configuration

Provide secrets via a local `.env` (gitignored) - never commit them. `docker compose`
interpolates `.env` into the service environments.

| Variable | Required | Purpose |
|----------|----------|---------|
| `DATABASE_URL` | yes (compose sets a dev value) | Postgres + pgvector connection string |
| `TRANSCRIPTION_API_KEY` | yes | ElevenLabs Scribe v2 speech-to-text |
| `EMBEDDING_API_KEY` | no for seeding | Voyage AI `voyage-4` embeddings. Seeding reads the committed cache offline; this is needed only to embed live query/segment text or to run `make refresh-embeddings` |
| `AUTH_EMAIL` | yes | Operator login email (single user, no registration) |
| `AUTH_PASSWORD_HASH` | yes | Encoded argon2id hash of the operator password |
| `SESSION_SECRET` | yes | HMAC key for session cookies, at least 32 bytes |
| `SESSION_TTL` | no (default `24h`) | Session lifetime (Go duration) |
| `AUTH_INSECURE_COOKIE` | no (compose sets `true`) | Allow a non-Secure cookie, plain-HTTP local dev only |
| `BACKEND_URL` | no (compose sets it) | Frontend-side rewrite target for the same-origin `/api` and `/demo` proxy |
| `PORT` | no (default `8080`) | Backend listen port |
| `CORS_ALLOWED_ORIGIN` | no | Browser origin allowed to call the API cross-origin. Leave unset: the session cookie is `SameSite=Strict`, so authenticated calls must be same-origin (the dev proxy / the ALB) |
| `DEMO_MEDIA_DIR` | no (default `demo`) | Directory the backend serves and transcribes the demo clip from |
| `EMBEDDING_MODEL` | no (default `voyage-4`) | Voyage embedding model; the same value must be used for ingest and query |
| `EMBEDDING_DIM` | no | If set, must equal the pinned index dimension (1024); a mismatch fails fast rather than silently re-ingesting |
| `TRANSCRIPTION_MODEL` | no (default `scribe_v2`) | ElevenLabs batch speech-to-text model |
| `MATCH_TOP_K`, `MATCH_SCORE_THRESHOLD`, `MATCH_EMBED_CONCURRENCY`, `MATCH_TIMEOUT` | no | Matching tuning (see `internal/config`) |

The same embedding model must be used for ingest and query, so `EMBEDDING_MODEL` (default
`voyage-4`) and the pinned 1024-dim index are shared by both paths.

Generate the operator credential secrets - only the hash and the secret are ever configured,
never the plaintext password:

```bash
cd stack/backend
printf '%s' "your-password" | go run ./cmd/genhash   # -> AUTH_PASSWORD_HASH
openssl rand -hex 32                                  # -> SESSION_SECRET
```

In `.env`, single-quote the hash so docker-compose does not expand its `$` signs.

Sessions are stateless HMAC tokens: logout clears the browser cookie but cannot revoke a
stolen token. To invalidate every outstanding session immediately, rotate `SESSION_SECRET`
and restart the backend.

## Demo

The bundled demo clip (`stack/backend/demo/`) narrates several well-known claims. The backend
serves it at `/demo/<file>` (sign in first - demo media sits behind the session gate, and the
frontend proxies the path same-origin) so the browser plays exactly the file the pipeline
transcribes; the panel shows each segment's nearest curated claims with a `corroborates`,
`contradicts`, or `unclear` verdict and source links, in sync with playback. In the seeded
local environment the panel is served from precomputed results
(`stack/backend/seed/demo_results.json`), so the demo works with no API keys; against a real
upload the same panel is filled by the live transcribe-embed-match pipeline, and a failed
provider call shows the error with a **Try again** button.

## Local development data

`make up` brings the stack up with a realistic, fully offline dataset. The same data is
managed with one-command reset and reseed targets (root `Makefile`):

| Command | What it does |
|---------|--------------|
| `make up` | Bring up the whole stack; migrate and seed run as one-shot steps |
| `make reset` | Soft reset: drop the schema, re-migrate, and reseed (seconds; container stays up) |
| `make reset-hard` | Discard the Postgres volume and rebuild everything from scratch |
| `make seed` | Reseed every dataset; idempotent (safe to re-run) |
| `make seed-claims` / `make seed-wiki` / `make seed-demo` | Seed one dataset for targeted testing |
| `make refresh-embeddings` | Regenerate the committed embedding cache from the fixtures via Voyage |

The offline seed loads claims, the Wikipedia subset, and the demo results - but not the curated
sample-video record, which the backend upserts on startup (`VideoService.EnsureSamples`). A soft
`make reset` rebuilds the schema without restarting the backend, so the video gallery is empty
until you `docker compose restart backend` (or use `make reset-hard`, which restarts the stack).

The datasets and their fixtures (`stack/backend/seed/`):

- **Curated claims** - `claims.json`, matched against spoken segments.
- **Wikipedia evidence subset** - `wiki_chunks.json`, a small set of chunks overlapping the
  demo so evidence lookups return something meaningful.
- **Demo-video results** - `demo_results.json`, precomputed `processed_videos` +
  `segment_results` keyed to the SHA-256 of the demo source string `common-myths.mp4`
  (video id `f0db671448289655a8b20f317a6336f4b53dc407448f5d5b2eb64b78c6577d80`), so the player
  and panel work end to end with no keys.

**Embeddings without an API key.** Each fixture's vector lives in a committed cache
(`embeddings.cache.jsonl`, keyed by model + input type + normalized text), so a full reseed is
offline and deterministic. The shipped cache holds deterministic placeholder vectors generated
without an API key; once you have a Voyage key, `make refresh-embeddings` replaces them with
real `voyage-4` vectors. After editing a fixture's text, run `make refresh-embeddings` (needs
`EMBEDDING_API_KEY`) so its cache entry is regenerated - otherwise an offline seed reports a
cache miss for the changed text. Transcribing a *new* (non-seeded) video still needs
`TRANSCRIPTION_API_KEY`; the demo never does.

Generate the operator credentials the login requires (see [Configuration](#configuration) for
detail):

```bash
cd stack/backend
printf '%s' "your-password" | go run ./cmd/genhash   # -> AUTH_PASSWORD_HASH
openssl rand -hex 32                                  # -> SESSION_SECRET
```

## Wikipedia corpus

Beyond the seeded claims, the verification store can be backed by a full Wikipedia corpus.
`make wikisync` (from `stack/backend`) downloads the corpus's multistream dump, extracts and
chunks each article's lead section, upserts the chunks, embeds every chunk via Voyage, then
swaps the freshly embedded corpus into place. The run is idempotent and an interrupted embed
resumes; `go run ./cmd/wikisync -dry-run` ingests and reports the embedding-cost estimate
without calling the embedding API or swapping anything.

**Populating over bounded sessions.** A full run is long and the embed step is paid, so you can
fill the corpus across several time-boxed runs:

```bash
cd stack/backend
make wiki-populate WIKI_MAX_DURATION=15m   # run for up to 15m, then stop; re-run to continue
```

`wiki-populate` runs the same bulk pipeline but stops cleanly at the budget, leaving the
embedded prefix committed; each re-run resumes where the last left off. The corpus only goes
**live** (atomic table swap) once a run completes the whole embed - partial runs accumulate
progress without serving it. Under the hood this is
`go run ./cmd/wikisync -mode=bulk -max-duration=<dur>`; a `0` budget (the default for
`make wikisync`) runs to completion. The Make-level knobs are `WIKI_MAX_DURATION` (default
`15m`) and `WIKI_DUMP_DIR` (default `/tmp/wikisync-dump`, reused across runs).

Against the Docker Compose stack the same bounded session runs as an opt-in service behind the
`wiki` profile, so a plain `docker compose up` never triggers the paid embed:

```bash
docker compose --profile wiki run --rm wiki-populate                 # one 15m session, resumable
WIKI_MAX_DURATION=30m docker compose --profile wiki run --rm wiki-populate
```

It waits for Postgres and the migrations, reads `EMBEDDING_API_KEY` from the root `.env`, and
keeps the downloaded dump in the `wiki-dump` named volume so re-runs resume. The knobs are the
same `WIKI_MAX_DURATION` (default `15m`, `0` runs to completion) and `WIKI_CORPUS` (default
`simplewiki`).

It needs `DATABASE_URL` and `EMBEDDING_API_KEY`, plus these optional knobs:

| Variable | Required | Purpose |
|----------|----------|---------|
| `WIKI_CORPUS` | no (default `simplewiki`) | Wikimedia dump name (`<lang>wiki`); interpolated into the download and source URLs |
| `WIKI_EMBED_BATCH_SIZE` | no (default `128`, max `1000`) | Chunks per Voyage embedding request |
| `WIKI_EMBED_CONCURRENCY` | no (default `4`) | Concurrent embedding requests |
| `WIKI_EMBED_MAX_RETRIES` | no (default `6`) | Retries per request when the API throttles |
| `WIKI_EMBED_MAINTENANCE_WORK_MEM` | no (default `512MB`) | Postgres `maintenance_work_mem` for the HNSW index build; raise it for `enwiki` |
| `WIKI_EMBED_MAX_PARALLEL_WORKERS` | no (default `7`) | Parallel workers for the index build |

## Tests

```bash
cd stack/backend  && go test -race ./...
cd stack/frontend && npm test
```

## Infrastructure

State lives in the S3 bucket `truth-in-stream-tfstate` (native S3 locking). The bucket must
be bootstrapped once before the first `terraform init` - see
[`stack/terraform/README.md`](stack/terraform/README.md). Then:

```bash
cd stack/terraform/dev && terraform init && terraform plan
```

## CI

- `pr.yml` - lint + test for frontend (Node) and backend (Go) on every PR.
- `terraform.yml` - fmt/validate/plan on PRs touching `stack/terraform/**`; applies `dev` on
  merge to `main` (requires the `AWS_ROLE_ARN` repo secret, GitHub OIDC).
- `deploy.yml` - on merge to `main`, builds backend + frontend images, scans them with Trivy
  (fails on HIGH/CRITICAL OS or library vulns), then pushes to GHCR with SBOM + provenance
  attestations. The AWS deploy step is a documented TODO pending the runtime target in Terraform.

## Claude Workflow

`.claude/` is the source of truth. `CLAUDE.md` holds always-on rules; per-stack knowledge
loads on demand via the `nextjs`, `go`, and `terraform` skills.

These workspace-specific slash commands drive day-to-day work:

| Command | Purpose |
|---------|---------|
| `/mayday` | Menu and router for this workspace's commands |
| `/roadmap` | Sync the roadmap state file from Linear and compute the ready queue |
| `/pick` | Claim the next ready card (parallel-safe) and deliver it in an isolated worktree |
| `/reconcile` | Rebase one or more card branches onto latest `main`, resolving conflicts per branch |
| `/card` | Create or update a Linear card following the workspace card rules |
| `/research` | Verify current best practice and latest version before integrating a pattern |
| `/version` | Compare the local `VERSION` file with the Linear version card |
| `/undersatnd` | Refresh the understand anything index for this repo and report status |
| `/setup` | Bootstrap a new project from this template (detach, scaffold, link a new repo) |

The repo is indexed by **GitNexus** for graph-aware code navigation:

- A SessionStart hook (`.claude/hooks/nexus-sync.sh`) refreshes the index in the background
  every session - non-blocking, index-only (never edits tracked files).
- Run **`/nexus`** for an on-demand refresh, or **`/nexus force`** for a full re-index.

### Parallel card delivery

Cards are delivered from Linear, one card per session, and several sessions can run at once:

1. **`/roadmap`** syncs Linear into the state file and computes a **ready queue** - the cards
   whose dependencies (`depends_on`) are all `Done`, ordered by priority then critical-path
   impact. This is the feature tracker that tells a session which card to pick next.
2. **`/pick`** (run it in each session) claims the top ready card. Claiming flips the card to
   `In Progress` and posts a `CLAIM <nonce>` comment; the earliest comment (by Linear's server
   timestamp) wins, so two sessions never take the same card. The winner gets an isolated
   worktree under `.claude/worktrees/<branch>` and delivers it (TDD -> `/code-review` ->
   PR -> In Review) without touching the others.
3. **`/reconcile`** rebases the per-card worktree branches onto latest `main` when they drift.
4. **`/pick --steal VER-x`** reclaims a card left `In Progress` by a session that died.

Rules live in the `roadmap-linear` and `delivering-linear-cards` skills.
