---
name: ci
description: Use when adding or changing CI/CD in .github/workflows - the reusable-workflow design, the pr.yml merge gate, terraform plan/apply gating, and the human-gated deploy engine
---

# CI/CD (.github/workflows)

Reusable, composable GitHub Actions. Thin trigger workflows wire events to
`workflow_call` building blocks; the building blocks own all the real logic.
NEVER duplicate steps across trigger workflows -- factor them into a `_`-prefixed
reusable workflow and call it. NEVER add a step to a trigger workflow that a
reusable one should own.

## Architecture

Two kinds of files, by naming convention:

- `_`-prefixed (`_lint.yml`, `_test.yml`, `_terraform.yml`, `_deploy.yml`) are
  `on: workflow_call` building blocks. They take typed `inputs:` (and `secrets:`
  where AWS is involved). They are NEVER triggered by an event directly.
- Un-prefixed files (`pr.yml`, `terraform.yml`, `deploy-*.yml`) are thin trigger
  workflows: they declare `on:` and a `jobs:` block that `uses:` a building
  block with `with:`. They carry almost no `run:` logic of their own.

The split exists so one tested job body (e.g. lint a stack) is reused across
every language and every stack via `inputs.lang`/`inputs.path`, and so a
permission-sensitive job (terraform, deploy) is defined once and called from
guarded triggers.

| Workflow | Trigger | Role |
|---|---|---|
| `pr.yml` | `pull_request` | Merge gate. Fans out lint+test for node+go, ~13 bash script unit tests, fmt + python gates. No path filters. |
| `_lint.yml` | `workflow_call` | gofmt + `go vet` + conditional `sqlc diff` + golangci-lint (go); eslint (node). |
| `_test.yml` | `workflow_call` | Spins `pgvector/pgvector:pg16`; `go test -race ./...` (go); `npm ci && npm test` (node). |
| `terraform.yml` | `push`/`pull_request` on `stack/terraform/**` | Path-filtered trigger that calls `_terraform.yml` for the `dev` env. |
| `_terraform.yml` | `workflow_call` | fmt-check, validate, plan, IAM apply-guard, `apply tfplan` on main only. |
| `_deploy.yml` | `workflow_call` | Deploy engine: OIDC -> ECR -> build -> Trivy scan -> push immutable `sha-` tag -> roll. |
| `deploy-{backend,frontend,backup}.yml` | `workflow_dispatch` only | Thin per-service wrappers around `_deploy.yml`. Human-gated. |

## The PR merge gate (`pr.yml`)

`pr.yml` is the single gate every PR must pass. It fans out into:

- `_lint.yml` + `_test.yml` for BOTH `stack/frontend` (node) and `stack/backend`
  (go), four jobs total via `with: { path, lang }`.
- ~13 bash unit-test jobs, one per ops script, each `bash scripts/<name>.test.sh`
  with a stubbed `aws`/`docker`/`psql` (no AWS account, no credentials, no
  daemon): `bootstrap-tfstate`, `iam-apply-guard`, `ssm-port-forward`,
  `db-tunnel`, `db-push`, `aws-target-guard`, `ingest-host`, `ingest-fetch-env`,
  `insee-idempotency-check`, `doctor`, `push-secrets`.
- `main-account-terraform-fmt`: `terraform fmt -check -recursive` ONLY against
  `stack/terraform/main-account`. That DNS root is applied by hand against the
  main account and is deliberately excluded from `terraform.yml` -- this job
  format-gates it without ever init/plan/apply or assuming a role.
- `slack-forwarder-test`: a Python `python -m unittest handler_test` for the
  CloudWatch-alarm-to-Slack forwarder lambda (boto3 stubbed); its only gate
  since the handler ships as a single file with no build step.

`pr.yml` deliberately has NO `paths:` filter. Cross-stack policy: every PR runs
the full matrix so a change in one stack cannot silently skip another stack's
checks. NEVER add a path filter to `pr.yml` to "save minutes" -- this is the same
all-or-nothing gate the `testing` skill describes. When you add an ops script,
add its `*.test.sh` job here in the same PR.

## Lint and test building blocks

`_test.yml` (`_lint.yml` mirrors the language switch):

```yaml
services:
  postgres:
    image: pgvector/pgvector:pg16   # go store integration tests; node ignores it
```

- go: `go test -race ./...` with
  `TEST_DATABASE_URL: postgres://postgres:postgres@localhost:5432/test?sslmode=disable`.
  Always `-race`.
- node: `npm ci && npm test --if-present`.

`_lint.yml` go path, in order: `gofmt -l .` must be empty, `go vet ./...`,
then `sqlc diff` ONLY when `hashFiles('{path}/sqlc.yaml') != ''` (skipped where
there is no sqlc config), then golangci-lint with `version-file:
.golangci-lint-version` -- NEVER hardcode the linter version, it is pinned in
that file (see the `go` skill). node path: `npm run lint --if-present`.

## Terraform gate (`terraform.yml` + `_terraform.yml`)

`terraform.yml` is path-filtered to `stack/terraform/**` plus the two workflow
files, and triggers on both `pull_request` and `push` to main. It calls
`_terraform.yml` for `dev` with `apply: ${{ github.ref == 'refs/heads/main' }}`.
prod is NOT auto-applied -- it is promoted by hand. NEVER wire a prod auto-apply.

The caller MUST grant `permissions: { id-token: write, contents: read }`
explicitly: a called workflow only receives permissions the caller already holds
and the repo default is `id-token: none`, so omitting it is a parse-time failure.

`_terraform.yml` runs, in order:

1. `terraform fmt -check -recursive` (always).
2. Fork PRs without creds (`env.AWS_ROLE_ARN == ''`): `init -backend=false` +
   `validate` only. No backend, no plan.
3. With creds: OIDC `configure-aws-credentials` assume -> `init` -> `validate` ->
   `plan -out=tfplan`.
4. IAM apply-guard: `terraform show -json tfplan` piped into
   `scripts/iam-apply-guard.sh` -- fails if the apply role lacks any action the
   plan needs, on EVERY credentialed plan (PR and main), so a missing permission
   is caught before merge, not mid-apply. See the `iam` skill.
5. `apply tfplan` ONLY when `inputs.apply && env.AWS_ROLE_ARN != ''` -- applies
   exactly the reviewed plan, never a re-plan. See the `terraform` skill.

## Deploy engine (`_deploy.yml`)

The deploy engine: OIDC auth (no static keys), `amazon-ecr-login`, build once
with `load: true`, Trivy scan, then push. Order matters -- the image is built and
scanned BEFORE it is published.

- Trivy `aquasecurity/trivy-action` runs with `exit-code: "1"` and
  `severity: HIGH,CRITICAL`. A HIGH or CRITICAL CVE BLOCKS the deploy. NEVER
  lower the severity or set `exit-code: "0"` to get a deploy out.
- `docker/metadata-action` produces an immutable `type=sha` tag (`sha-<7char>`)
  that the roll step pins to, plus `latest` only `enable={{is_default_branch}}`.
- Two `deploy_mode`s:
  - `rolling`: `aws ecs update-service --force-new-deployment` + `wait
    services-stable` (backend, frontend). Circuit breaker rolls back on failure.
  - `image-only`: build, scan, push, no roll (backup; a scheduled task picks it
    up on its next run).
- rolling + non-empty `migrate_dockerfile` (backend): build + scan + push a
  migrate image, then run a one-off Fargate `run-task`, wait `tasks-stopped`,
  and fail on a non-zero container exit. Skips cleanly when the migrate task
  definition is absent (no RDS).

## Deploy wrappers (`deploy-*.yml`)

`deploy-backend`, `deploy-frontend`, `deploy-backup` are thin
`workflow_dispatch`-ONLY wrappers that `uses: ./.github/workflows/_deploy.yml`
with the service's `service`/`ecr_repo`/`context`/`dockerfile`/`deploy_mode`.
Deploys are human-gated: an auto-merge to main NEVER deploys. NEVER add a `push`
trigger to a deploy wrapper while AWS is operator-gated.

## Conventions

- Pin every third-party action to a full commit SHA with a trailing `# vX.Y.Z`
  comment (`aws-actions/configure-aws-credentials@e7f100c... # v6`). First-party
  `actions/*` use a major tag (`@v4`). NEVER float a third-party action on a tag.
- AWS auth is GitHub OIDC role assumption only -- no static keys, no AWS secrets
  in YAML. Deploy config comes from repository VARIABLES (`vars.*`), not secrets;
  `_deploy.yml` preflights them and fails fast with a clear message.
- Least-privilege `permissions:` per job/workflow. Script-test jobs set
  `permissions: { contents: read }`. Grant `id-token: write` only where OIDC is
  used.
- NEVER interpolate untrusted `github.event.*` (PR titles, branch names, bodies)
  directly into a `run:` block -- pass it through `env:` and reference `$VAR`, so
  it is data, not shell.

## Pitfalls

1. Adding a path filter to `pr.yml` -- breaks the cross-stack all-or-nothing gate.
   Path filtering belongs on `terraform.yml`, not the merge gate.
2. Calling `_terraform.yml` or `_deploy.yml` without granting `id-token: write`
   in the caller -- parse-time rejection, since callees only inherit caller
   permissions and the repo default is `id-token: none`.
3. Floating a third-party action on a tag instead of a pinned SHA.
4. Lowering Trivy severity or `exit-code` to ship past a CVE gate.
5. Hardcoding the golangci-lint version instead of `version-file:
   .golangci-lint-version`.
6. Adding an ops script without its `scripts/<name>.test.sh` job in `pr.yml`.
7. Adding a `push` trigger to a `deploy-*.yml` wrapper -- deploys are
   `workflow_dispatch`-only and human-gated.
8. Forgetting the `main-account-terraform-fmt` gate when touching
   `stack/terraform/main-account` -- it is excluded from `terraform.yml` and
   only fmt-gated in `pr.yml`.
