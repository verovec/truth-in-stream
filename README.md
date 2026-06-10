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

The fact-check pipeline calls two external providers, so the demo needs API keys:

```bash
cp .env.example .env
# then set TRANSCRIPTION_API_KEY (ElevenLabs) and EMBEDDING_API_KEY (Voyage) in .env
```

Bring up the whole stack (Postgres, schema migration, claim ingestion, backend, frontend):

```bash
docker compose up
# frontend -> http://localhost:3000
# backend  -> http://localhost:8080/healthz
```

`docker compose up` runs, in order: Postgres, a one-shot `migrate`, a one-shot `ingest` that
embeds the seeded claims into pgvector, then the backend and frontend. Open
http://localhost:3000 - the bundled demo clip loads, the backend transcribes it, embeds and
matches each segment against the seeded claims, and the fact-check panel fills in as the clip
plays. See [Demo](#demo).

## Configuration

Provide secrets via a local `.env` (gitignored) - never commit them. `docker compose`
interpolates `.env` into the service environments.

| Variable | Required | Purpose |
|----------|----------|---------|
| `DATABASE_URL` | yes (compose sets a dev value) | Postgres + pgvector connection string |
| `TRANSCRIPTION_API_KEY` | yes | ElevenLabs Scribe v2 speech-to-text |
| `EMBEDDING_API_KEY` | yes | Voyage AI `voyage-4` embeddings - used by both `ingest` and query |
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
`contradicts`, or `unclear` verdict and source links, in sync with playback. Seeded claims
live in `stack/backend/seed/claims.json`; re-running `ingest` is idempotent. If a provider
call fails, the panel shows the error with a **Try again** button.

## Wikipedia corpus

Beyond the seeded claims, the verification store can be backed by a full Wikipedia corpus.
`make wikisync` (from `stack/backend`) downloads the corpus's multistream dump, extracts and
chunks each article's lead section, upserts the chunks, embeds every chunk via Voyage, then
swaps the freshly embedded corpus into place. The run is idempotent and an interrupted embed
resumes; `go run ./cmd/wikisync -dry-run` ingests and reports the embedding-cost estimate
without calling the embedding API or swapping anything.

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
| `/nexus` | Refresh the GitNexus index for this repo and report status |
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
