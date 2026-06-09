---
name: testing
description: Use when running the test suite, finishing or integrating a feature card, deciding what must pass before a PR, adding or changing CI workflow jobs, or when a pipeline job is red or flaky
---

# Full-suite testing (whole codebase, every card)

Core rule: **integrating a feature card means the entire suite is green on BOTH stacks** - locally before the PR opens, and in CI on the PR. There is no "my card only touched the backend" exemption: a backend change can break the frontend's contract and vice versa, which is why the pipeline has no path filters and you don't get one either.

## The gate

Every PR runs four jobs (`.github/workflows/pr.yml`): `frontend-lint`, `frontend-test`, `backend-lint`, `backend-test`. `deploy.yml` re-runs the same four before any image is built, then Trivy-scans images and blocks on HIGH/CRITICAL vulnerabilities.

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

All eight commands pass or the PR does not open. `make test` alone is not the full backend suite - the store integration tests only run with a database (`docker compose up postgres` provides `pgvector/pgvector:pg16`, same image as CI).

## CI contract (what the reusable workflows guarantee)

- `_test.yml`: node -> `npm ci && npm test`; go -> `go test -race ./...` with a `pgvector/pgvector:pg16` service and `TEST_DATABASE_URL` injected. So **store integration tests always run in CI** - the env-var skip is a local convenience only, never a way to dodge them.
- `_lint.yml`: node -> eslint; go -> gofmt-check + `go vet` + `sqlc diff` (auto-skipped when no `sqlc.yaml`) + golangci-lint.
- Extending CI: a new stack or check is two `uses:` entries in `pr.yml` reusing `_lint.yml`/`_test.yml` (and mirrored into `deploy.yml`'s gate), never a bespoke one-off workflow. Never add `paths:` filters to `pr.yml` - the whole-codebase gate is the point. Pin tool versions (Go, Node, sqlc) identically in CI, Makefile, and local docs; a version drift between them is a finding.

## What every card ships (test side of Definition of Done)

Tests land in the same diff as the behaviour - new logic with no new tests does not merge.

- Go: handlers via `httptest.NewRecorder` against the `newMux()` constructor; services table-driven; store changes covered by the integration test against real pgvector (a mocked-pool store test proves nothing about SQL or HNSW behaviour). Conventions in the go skill apply (`t.Context()`, `t.Parallel()`, `b.Loop()`, no testify).
- Frontend: Server Actions, the data-access layer, and Zod schemas as plain async-function tests; client leaves via `@testing-library/react`; async Server Components by extracting the data logic into a tested function (RTL cannot render them - nextjs skill).
- Placement: Go `*_test.go` beside the code, same package; frontend `*.test.tsx`/`*.test.ts` colocated with the file under test.

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
