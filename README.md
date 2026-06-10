# truth-in-stream

Real-time fact-checking for live streams.

## Stack

| Layer | Tech | Location |
|-------|------|----------|
| Frontend | Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) | `stack/frontend` |
| Backend | Go (standard-library `net/http` service) | `stack/backend` |
| Data | Ladybug (vector store; embeddings generated externally) + external transcription / embedding providers | _planned, see Configuration_ |
| Infra | Terraform on AWS, region `eu-west-3` | `stack/terraform` |

## Quick start

Local development (both services, hot reload):

```bash
docker compose up
# frontend -> http://localhost:3000
# backend  -> http://localhost:8080/healthz
```

Or run each service directly:

```bash
# backend
cd stack/backend && go run ./cmd/server

# frontend
cd stack/frontend && npm install && npm run dev
```

## Configuration

The current backend reads only `PORT` (default `8080`). The fact-checking pipeline is the next
build-out and will require these provider credentials (not yet wired):

| Variable | Purpose |
|----------|---------|
| `TRANSCRIPTION_API_KEY` | External speech-to-text for the live stream |
| `EMBEDDING_API_KEY` | External embedding generation for the Ladybug vector store |

Provide them via environment / a local `.env` (never commit secrets). Ladybug stores vectors
only - embeddings are produced by the external provider, not in-process.

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

## Working with Claude Code

`.claude/` is the source of truth. `CLAUDE.md` holds always-on rules; per-stack knowledge
loads on demand via the `nextjs`, `go`, and `terraform` skills.

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
