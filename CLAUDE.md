# truth-in-stream

Real-time fact-checking for live streams. The workspace is the monorepo; project code
lives under `stack/`.

## Stack
- Frontend: Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) - `stack/frontend`
- Backend: Go (stdlib net/http service) - `stack/backend`
- Infra: Terraform on AWS, region `eu-west-3`, directory-per-env - `stack/terraform`
- Data: Postgres + pgvector (vector store, `DATABASE_URL`) + embeddings via Voyage AI `voyage-4-large`
  API (`EMBEDDING_API_KEY`), index pinned to 1024 dims (`halfvec(1024)`, HNSW cosine). Same model
  for ingest (`input_type=document`) and query (`input_type=query`)
- Speech-to-text: AssemblyAI Universal-3 Pro streaming (`TRANSCRIPTION_API_KEY`), the single
  transcriber. Live streams and imported videos alike stream their audio over its realtime
  diarizing WebSocket; there is no batch transcription path.
- Local dev: `docker-compose.yml` (postgres :5432, backend :8080, frontend :3000)

## Always-on rules
- Deploys stay human-gated. Rolling the prod services is done by pushing a semver tag (`v*`) whose
  commit is on `main`: that fires `release.yml`, which rolls backend (with migrations), Keycloak
  (with its DB bootstrap), and frontend. Applying the prod infrastructure
  (`terraform apply stack/terraform/prod`, including standing the stack up the first time) is a
  separate, deliberate human step, kept out of the tag path on purpose. Never run `terraform apply`
  by hand, dispatch a `deploy-*.yml` workflow, push a deploy tag, or take any other
  production-affecting action on the user's behalf without explicit approval; an agent delivers to
  `main` but never tags a release or applies prod.
- Merging a card's PR to `main` is automatic in this workspace once the PR's CI is green. After a
  passing code-review and a green end-to-end check, the delivering agent rebases on `main`, opens
  the PR, waits for CI to pass, merges, and moves the card to Done (see the `delivering-linear-cards`
  skill and the `/pick` command). This project-scoped rule supersedes the global `ship-after-review`
  skill's "merge stays human-gated" clause for this repo only. An auto-merge to `main` never
  triggers a deploy; only a version tag does (the `deploy-*.yml` wrappers stay `workflow_dispatch`
  for ad-hoc single-service rolls).
- Best practice and long-term maintainability first.
- Before integrating a new library/pattern/tool, verify current best practice and latest
  stable version via Context7 (web search if needed) before writing code.
- This repository is PUBLIC on GitHub. Never commit secrets or infrastructure identifiers
  (AWS account ids, account-bearing ARNs, internal endpoints); keep real values in gitignored
  local files (`deploy/targets.json`, `*.tfvars`, `.env`), env, or a secrets store, and leave
  placeholders + a `.example` in the tree. Run `scripts/secret-scan.sh` before committing. See the
  public-repo-hygiene skill.
- Never use emojis. No comments that restate code. Only touch files needed for the task.
- No code without tests. Every behaviour change ships with its tests in the same change,
  and CI runs them on every PR and before every deploy. Untestable code is a design
  problem to fix, not a reason to skip the test.

## Engineering standards (apply to every change, every agent)
- Verify the latest stable version and current best practice via Context7 before adding or
  upgrading any library, pattern, or tool. Record the version and decision in the per-stack skill.
- Respect architecture boundaries. Go backend is layered `cmd/server` (wiring only) ->
  `internal/handler` (HTTP only) -> `internal/service` (no HTTP types) -> `internal/store`.
  Frontend defaults to Server Components; `'use client'` only at the leaf that needs it.
- Secrets come from env/config only. Never hard-code, commit, or log them.
- Tests are required for new logic. Go: table-driven, `go test -race ./...` green.
  Frontend: Vitest. Cover the behaviour the card adds.
- Leave it clean before Done. Go: `gofmt`/`gofumpt`, `go vet ./...`, `golangci-lint run ./...`.
  Frontend: ESLint. Wrap Go errors with `%w`; delete dead code instead of commenting it out.

## Definition of Done (no card is Done until all pass)
- Acceptance criteria met; tests green (`-race` for Go); an end-to-end check of the feature passes;
  lint and format clean.
- A code review has been run on the change and every correctness finding resolved.
- The branch is rebased on latest `main`, the PR's CI is green, and the PR is merged to `main`; the
  card is then moved to Done. Deploys still require explicit human approval.

## Code review (mandatory, every card)
Every card carries a code-review gate and it is never optional. Run a review on the diff,
resolve all correctness findings, address or justify quality findings, then re-review after
changes. A card is not Done until its review passes.

## On-demand context (load only when relevant)
- Next.js frontend -> nextjs skill.
- Go backend -> go skill.
- Running/adding tests, finishing a card, CI checks -> testing skill.
- Terraform / AWS -> terraform skill.
- Committing, or handling secrets / infra identifiers on this public repo -> public-repo-hygiene skill.
- Writing/reviewing code -> coding-philosophy skill.
- Updating the README / `docs/` after an epic completes -> maintaining-documentation skill.
- Linear cards / roadmap -> roadmap-linear skill; commands `/roadmap`, `/card`.
- Implementing a card / parallel card delivery -> delivering-linear-cards skill; commands `/pick`, `/reconcile`.
- Integrating a new pattern -> research-patterns skill; command `/research`.

## Commands
`/roadmap`, `/pick`, `/card`, `/research`, `/version`, `/reconcile`, `/mayday` (router).

## State
`.factory-state.json` (gitignored) holds identity, Linear ids, and the `stack` choices.

<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **truth-in-stream** (6224 symbols, 15283 relationships, 251 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> If any GitNexus tool warns the index is stale, run `npx gitnexus analyze` in terminal first.

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `gitnexus_impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `gitnexus_detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `gitnexus_query({query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `gitnexus_context({name: "symbolName"})`.

## Never Do

- NEVER edit a function, class, or method without first running `gitnexus_impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `gitnexus_rename` which understands the call graph.
- NEVER commit changes without running `gitnexus_detect_changes()` to check affected scope.

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/truth-in-stream/context` | Codebase overview, check index freshness |
| `gitnexus://repo/truth-in-stream/clusters` | All functional areas |
| `gitnexus://repo/truth-in-stream/processes` | All execution flows |
| `gitnexus://repo/truth-in-stream/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->
