# Development

Tests, continuous integration, and the Claude-assisted delivery workflow.

## Tests

```bash
cd stack/backend  && go test -race ./...
cd stack/frontend && npm test
./scripts/doctor.test.sh            # local-stack preflight (stubs the docker CLI)
./scripts/secrets_test.sh           # operator secrets tool (stubs AWS + editor)
./scripts/iam-apply-guard.test.sh   # pre-apply IAM guard (stubs the aws CLI)
```

Every behaviour change ships with its tests in the same change. Go tests are table-driven and must
pass under `-race`; the frontend uses Vitest. See the `testing` skill for the full gate.

## CI

- `pr.yml` - lint + test for frontend (Node) and backend (Go) on every PR.
- `terraform.yml` - fmt/validate/plan on PRs touching `stack/terraform/**`; applies `dev` on merge to
  `main` (GitHub OIDC, `AWS_ROLE_ARN`).
- Per-service deploy workflows (`deploy-backend.yml`, `deploy-frontend.yml`, `deploy-workers.yml`,
  `deploy-backup.yml`), each `workflow_dispatch`-only (human-gated) and calling the reusable
  `_deploy.yml`. A pipeline builds, Trivy-scans, and pushes one service's image to ECR with an
  immutable `sha-<short>` tag, then rolls that service alone and waits for stability (backend also
  builds/scans the migrate image and runs migrations; workers roll the ingestion fleet via the
  worker-lifecycle lambda; backup is image-only, picked up by the scheduled task on its next run).
  No long-lived AWS credentials (GitHub OIDC); a production deploy is always a deliberate manual
  dispatch.

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
