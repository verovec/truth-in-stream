# truth-in-stream

Real-time fact-checking for live streams.

## Stack

| Layer | Tech | Location |
|-------|------|----------|
| Frontend | Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) | `stack/frontend` |
| Backend | Go (standard-library `net/http` service) | `stack/backend` |
| Data | Postgres 16 + `pgvector` (vector store), Voyage AI `voyage-4-large` embeddings, AssemblyAI Universal-3 Pro streaming transcription (the single transcriber, live streams and imported videos alike) | `stack/backend` |
| Infra | Terraform on AWS, region `eu-west-3` | `stack/terraform` |

## Quick start

From a clean clone to the demo playing in the browser is three commands. The local dataset (curated
claims, a Wikipedia evidence subset, demo-video results) seeds **fully offline** from a committed
embedding cache, so **no real API keys are needed to bring the stack up and play the demo**.

**Prerequisites:** Docker Engine 24.0+ with the Compose v2 plugin (`docker compose version`), GNU
make, and a few GB of free disk. The Go toolchain is used only by `make bootstrap` to generate
operator credentials, not to run the stack.

```bash
make doctor      # optional: preflight Docker, Compose v2, make, and the daemon
make bootstrap   # generate .env: operator email, argon2id password hash, session secret
make up          # build and start the whole stack
# frontend -> http://localhost:3000
# backend  -> http://localhost:8080/healthz
```

`make bootstrap` copies `.env.example` to `.env` (when absent), fills the three auth secrets that
have no safe default (`AUTH_EMAIL`, `AUTH_PASSWORD_HASH`, `SESSION_SECRET`), and writes
self-describing placeholders for `TRANSCRIPTION_API_KEY` / `EMBEDDING_API_KEY` so a fresh clone boots
and plays the offline demo. It is idempotent and never writes the plaintext password to disk. Replace
the placeholders with real keys only when you move on to live analysis. See
[Configuration](#configuration) to set `.env` by hand.

`make up` runs, in order: Postgres, a one-shot `migrate`, a one-shot offline `seed`, then the backend
and frontend. Open <http://localhost:3000>, sign in with the operator credentials, and the bundled
demo clip plays with the fact-check panel populated from seeded results - no provider call.

## Configuration

Secrets come from a local `.env` (gitignored); `docker compose` interpolates it. Full tuning lives in
`stack/backend/internal/config`. The essentials:

| Variable | Required | Purpose |
|----------|----------|---------|
| `DATABASE_URL` | yes (compose sets a dev value) | Postgres + pgvector connection string |
| `TRANSCRIPTION_API_KEY` | yes (live only) | AssemblyAI key. `u3-rt-pro` streaming is the single transcriber for live streams and imported videos. Not used by the offline demo |
| `EMBEDDING_API_KEY` | no for seeding | Voyage `voyage-4-large`. Seeding is offline; needed only for live query embedding, `make refresh-embeddings`, or wiki ingestion |
| `EMBEDDING_MODEL` | no (default `voyage-4-large`) | Must match between ingest and query (different models = different vector spaces); pinned to 1024 dims. Changing it requires `make refresh-embeddings` |
| `AUTH_EMAIL` / `AUTH_PASSWORD_HASH` / `SESSION_SECRET` | yes | Operator login (single user), argon2id hash, and HMAC session key (>= 32 bytes) |
| `SESSION_TTL` | no (default `24h`) | Session lifetime |
| `CORS_ALLOWED_ORIGIN` | no | Leave unset for same-origin dev (session cookie is `SameSite=Strict`) |
| `PORT` | no (default `8080`) | Backend listen port |

`make bootstrap` generates the three auth values and is the recommended path. By hand:

```bash
cd stack/backend
printf '%s' "your-password" | go run ./cmd/genhash   # -> AUTH_PASSWORD_HASH (single-quote it in .env)
openssl rand -hex 32                                  # -> SESSION_SECRET
```

Sessions are stateless HMAC tokens: to revoke every outstanding session, rotate `SESSION_SECRET` and
restart the backend.

## Local development data

`make up` seeds a realistic, fully offline dataset (curated claims, a Wikipedia evidence subset, demo
results) from a committed embedding cache. Manage it with one-command targets:

| Command | What it does |
|---------|--------------|
| `make reset` | Soft reset: drop the schema, re-migrate, reseed (seconds; container stays up) |
| `make reset-hard` | Discard the Postgres volume and rebuild from scratch |
| `make seed` | Reseed every dataset; idempotent |
| `make seed-claims` / `make seed-wiki` / `make seed-videos` | Seed one dataset for targeted testing |
| `make refresh-embeddings` | Regenerate the committed embedding cache from fixtures via Voyage (needs `EMBEDDING_API_KEY`) |

The shipped cache holds deterministic placeholder vectors, so a full reseed is offline and
deterministic; `make refresh-embeddings` swaps in real `voyage-4-large` vectors once you have a key.
A soft `make reset` rebuilds the schema without restarting the backend, so the sample-video gallery
(upserted on startup by `VideoService.EnsureSamples`) is empty until `docker compose restart backend`
or a `make reset-hard`.

## Ingestion pipeline (Wikipedia corpus)

Beyond the seeded claims, the verification store can be backed by a full Wikipedia evidence corpus,
built and embedded by an opt-in worker fleet behind the `wiki` Compose profile (paid; a plain
`make up` never starts it). Local quick start:

```bash
make fleet-up                                          # broker + embedding workers (long-running)
docker compose --profile tools run --rm wiki-populate \
  go run ./cmd/wikisync -mode=bulk -dry-run            # free cost estimate first
make wiki-populate                                     # ingest + embed + swap live (paid, resumable)
make wiki-verify                                       # green = corpus complete and consistent
make fleet-down
```

**For the architecture, diagrams, every mode and knob, vector-consistency guarantees, the cloud /
production path, and troubleshooting, see [`docs/ingestion-pipeline.md`](docs/ingestion-pipeline.md).**

## Tests

```bash
cd stack/backend  && go test -race ./...
cd stack/frontend && npm test
./scripts/doctor.test.sh            # local-stack preflight (stubs the docker CLI)
./scripts/secrets_test.sh           # operator secrets tool (stubs AWS + editor)
./scripts/iam-apply-guard.test.sh   # pre-apply IAM guard (stubs the aws CLI)
```

## Infrastructure

Operator tooling targets AWS through one SSO profile (`truth-in-stream-dev`, region `eu-west-3`).
State lives in the S3 bucket `truth-in-stream-tfstate` (native S3 locking). Dev provisions no RDS by
default (`enable_rds = false`); the database is developed locally.

```bash
./scripts/bootstrap-tfstate.sh                          # once, before the first init
cd stack/terraform/dev && terraform init && terraform plan
```

See [`stack/terraform/README.md`](stack/terraform/README.md) for the SSO setup, the CI/CD roles and
pre-apply IAM guard, and the `enable_rds` toggle.

- **Database backups** - the DB holds expensive-to-recompute embeddings, so it is dumped with
  `pg_dump -Fc` and restored without re-embedding (`halfvec` round-trips byte-for-byte). Manual:
  `make backup` / `make restore` (set `DB_BACKUP_BUCKET`). Scheduled: a Fargate cron task gated by
  `enable_db_backup`. See [`modules/scheduled-task`](stack/terraform/modules/scheduled-task/README.md).
- **Secrets** - Terraform creates the secret containers; values are set out of band with
  `./scripts/secrets.sh dev` (no value ever passes through an argv, log, or chat). ECS consumes
  secrets by ARN, so a roll needs no task-definition re-pin.
- **Cloud ingestion** (producer Fargate task, worker fleet, versioned queue, SSM bastion drain) is
  documented in [`docs/ingestion-pipeline.md`](docs/ingestion-pipeline.md#10-cloud--production-pipeline).

## CI

- `pr.yml` - lint + test for frontend (Node) and backend (Go) on every PR.
- `terraform.yml` - fmt/validate/plan on PRs touching `stack/terraform/**`; applies `dev` on merge to
  `main` (GitHub OIDC, `AWS_ROLE_ARN`).
- `deploy.yml` - `workflow_dispatch`-only (human-gated). Builds, Trivy-scans, and pushes images to
  ECR, runs migrations, rolls the backend/frontend services, and ships the ingestion pipeline. No
  long-lived AWS credentials; a production deploy is always a deliberate manual dispatch.

## Documentation and references

| Topic | Where |
|-------|-------|
| Always-on rules and engineering standards | [`CLAUDE.md`](CLAUDE.md) |
| Ingestion pipeline (local + cloud, diagrams, consistency) | [`docs/ingestion-pipeline.md`](docs/ingestion-pipeline.md) |
| Data dictionary (Postgres + pgvector) | `.claude/skills/data-map/SKILL.md` |
| Infrastructure (Terraform, AWS, CI/CD roles) | [`stack/terraform/README.md`](stack/terraform/README.md) |
| Design specs | `docs/superpowers/specs/` |
| Per-stack conventions | the `go`, `nextjs`, `terraform`, `testing` skills |

## Claude workflow

`.claude/` is the source of truth: `CLAUDE.md` holds always-on rules, and per-stack knowledge loads
on demand via skills. Cards are delivered from Linear, one per session, several at once. Day-to-day
slash commands:

| Command | Purpose |
|---------|---------|
| `/mayday` | Menu and router for this workspace's commands |
| `/roadmap` | Sync the roadmap state file from Linear and compute the ready queue |
| `/pick` | Claim the next ready card (parallel-safe) and deliver it in an isolated worktree |
| `/reconcile` | Rebase one or more card branches onto latest `main` |
| `/card` | Create or update a Linear card |
| `/research` | Verify current best practice and latest version before integrating a pattern |
| `/version` | Compare the local `VERSION` file with the Linear version card |
| `/nexus` | Refresh the GitNexus code-intelligence index (`/nexus force` for a full re-index) |
| `/setup` | Bootstrap a new project from this template |

Rules live in the `roadmap-linear` and `delivering-linear-cards` skills.
