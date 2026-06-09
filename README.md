# truth-in-stream

Real-time fact-checking for live streams.

## Stack

| Layer | Tech | Location |
|-------|------|----------|
| Frontend | Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) | `stack/frontend` |
| Backend | Go (standard-library `net/http` service) | `stack/backend` |
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
- `deploy.yml` - builds and pushes backend + frontend images to GHCR on merge to `main`. The
  AWS deploy step is a documented TODO pending the runtime target in Terraform.

## Working with Claude Code

`.claude/` is the source of truth. `CLAUDE.md` holds always-on rules; per-stack knowledge
loads on demand via the `nextjs`, `go`, and `terraform` skills.
