---
name: testing
description: Use when running the test suite, finishing or integrating a feature card, deciding what must pass before a PR, adding or changing CI workflow jobs, or when a pipeline job is red or flaky
---

# Full-suite testing (whole codebase, every card)

Core rule: **integrating a feature card means the entire suite is green on BOTH stacks** - locally before the PR opens, and in CI on the PR. There is no "my card only touched the backend" exemption: a backend change can break the frontend's contract and vice versa, which is why the pipeline has no path filters and you don't get one either.

## The gate

Every PR runs four jobs (`.github/workflows/pr.yml`): `frontend-lint`, `frontend-test`, `backend-lint`, `backend-test`. The green PR CI is the merge gate. Deploys are separate, human-gated, per-service workflows (`deploy-backend.yml`, `deploy-frontend.yml`, `deploy-backup.yml`, all `workflow_dispatch`-only, calling the reusable `_deploy.yml`); each Trivy-scans its image and blocks on HIGH/CRITICAL vulnerabilities before pushing it. They build one service alone and do not re-run the cross-stack suite, so the PR gate is the test/lint authority - never merge on red CI expecting a deploy to re-check.

A red job means the card is not done. Banned responses to red: merging anyway, `t.Skip`/`it.skip` to get green, deleting or loosening the failing test in the same diff as the feature that broke it, re-running until a flaky test passes. A flaky test is a bug - file a card and fix the root cause (Go: `-race` + `testing/synctest`; frontend: no timing-based assertions).

## Local full suite (run before every PR - this mirrors CI exactly)

Backend, from `stack/backend`:

```sh
make fmt && make vet            # gofmt + go vet (CI: _lint.yml)
golangci-lint run ./...         # CI: golangci-lint-action
make sqlc-verify                # sqlc diff - required whenever SQL/migrations changed
make test                       # go test -race ./... (integration tests skip w/o DB)
make test-integration           # same suite against real pgvector (compose postgres up)
```

Frontend, from `stack/frontend`:

```sh
npm run lint                    # eslint (CI: _lint.yml)
npx tsc --noEmit                # type-check - run even though CI lacks this job yet
npm test                        # vitest run (CI: _test.yml)
```

All eight commands pass or the PR does not open. `make test` alone is not the full backend suite - the store integration tests only run with a database: `docker compose up -d postgres` (root compose file, `pgvector/pgvector:pg16`, same image as CI) matches the Makefile's default `DATABASE_URL` exactly, so `make test-integration` then works with no flags. Note `make sqlc-verify`, `migrate-*`, and the compose database all need Docker running - that is by design (pinned tooling, no local installs).

## CI contract (what the reusable workflows guarantee)

- `_test.yml`: node -> `npm ci && npm test --if-present` (caveat: `--if-present` silently passes if the `test` script is missing - never remove or rename the `test` script in package.json); go -> `go test -race ./...` with a `pgvector/pgvector:pg16` service and `TEST_DATABASE_URL` injected. So **store integration tests always run in CI**; the env-var skip exists solely so a unit-test run works before `docker compose up` - it is never a sanctioned way to avoid running them before a PR.
- `_lint.yml`: node -> eslint; go -> gofmt-check + `go vet` + `sqlc diff` (auto-skipped when no `sqlc.yaml`) + golangci-lint.
- Extending CI: a new stack or check is two `uses:` entries in `pr.yml` reusing `_lint.yml`/`_test.yml`, never a bespoke one-off workflow. A new deployable service is a thin per-service caller of the reusable `.github/workflows/_deploy.yml`, never a hand-rolled deploy job. ANY workflow change - new job, new reusable workflow, new gap-fix - is its own card, never a rider on a feature card. Never add `paths:` filters to `pr.yml` - the whole-codebase gate is the point. Pin tool versions (Go, Node, sqlc) identically in CI, Makefile, and local docs; a version drift between them is a finding.

## What every card ships (test side of Definition of Done)

Tests land in the same diff as the behaviour - new logic with no new tests does not merge.

- Go: handlers via `httptest.NewRecorder` against the `newMux()` constructor; services table-driven; store changes covered by the integration test against real pgvector (a mocked-pool store test proves nothing about SQL or HNSW behaviour). Conventions in the go skill apply (`t.Context()`, `t.Parallel()`, `b.Loop()`, no testify).
- Frontend: Server Actions, the data-access layer, and Zod schemas as plain async-function tests; client leaves via `@testing-library/react`; async Server Components by extracting the data logic into a tested function (RTL cannot render them - nextjs skill).
- Placement: Go `*_test.go` beside the code, same package; frontend `*.test.tsx`/`*.test.ts` colocated with the file under test.

## Local stack baseline (verified versions)

The local stack and every Docker-backed target (`make up`, `make test-integration`,
`make sqlc-verify`, `migrate-*`) require **Docker Engine 24.0+ with the Compose v2 plugin** (the
`docker compose` subcommand, never the legacy `docker-compose` binary). Engine 24.0+ bundles
Compose v2, which is the floor for the `profiles`, `--scale`, and `depends_on` healthcheck
conditions the root `docker-compose.yml` uses. Baseline verified 2026-06 against the Docker
documentation via Context7; re-verify before raising it. `make doctor` (`scripts/doctor.sh`, with a
`scripts/doctor.test.sh` companion) is the canonical preflight - run it when a stack will not start,
and keep its checks and the README Prerequisites in lockstep with this floor. Operator credentials
for a fresh checkout come from `make bootstrap` (`cmd/bootstrap`, which reuses
`service.HashOperatorPassword` so genhash and bootstrap hash identically); never hand-roll a second
argon2id path.

## Known gaps (harden via dedicated cards, never silently inside a feature card)

1. No `tsc --noEmit` CI job - type errors can pass CI today; the local step above is the stopgap.
2. No `next build` on PRs - build-only breakage (typed routes, RSC boundary violations) surfaces at deploy.
3. No Playwright E2E - async Server Component rendering is currently untested end-to-end.
4. No coverage threshold in either stack.

Each fix is its own card with its own review; when one lands, update this skill and delete the entry.

## Red flags - stop and fix

- "Only X changed, skipping Y's suite" - the pipeline runs both; so do you.
- A `t.Skip`, `it.skip`, `--no-verify`, or retry-until-green anywhere near a red check.
- Opening a PR before the eight local commands ran.
- A new CI job hand-rolled instead of reusing `_lint.yml`/`_test.yml`; a `paths:` filter creeping into `pr.yml`.
- "The integration test needs Docker so I'll rely on CI" - CI is the net, not the first run.
