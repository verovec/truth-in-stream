# truth-in-stream

Real-time fact-checking for live streams. The workspace is the monorepo; project code
lives under `stack/`.

## Stack
- Frontend: Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) - `stack/frontend`
- Backend: Go (stdlib net/http service) - `stack/backend`
- Infra: Terraform on AWS, region `eu-west-3`, directory-per-env - `stack/terraform`
- Data: Postgres + pgvector (vector store, `DATABASE_URL`) + embeddings via Voyage AI `voyage-4`
  API (`EMBEDDING_API_KEY`), index pinned to 1024 dims (`halfvec(1024)`, HNSW cosine). Same model
  for ingest (`input_type=document`) and query (`input_type=query`)
- Speech-to-text: ElevenLabs Scribe v2 API (`TRANSCRIPTION_API_KEY`). Batch REST for v1,
  Scribe v2 Realtime WebSocket for live. Both sit behind one `Transcriber` interface.
- Local dev: `docker-compose.yml` (postgres :5432, backend :8080, frontend :3000)

## Always-on rules
- Never merge or deploy on your own; always ask for explicit approval first. Pushing a feature
  branch and opening its PR after a passing code-review needs no approval (see the
  `ship-after-review` skill); only merge and deploy stay human-gated.
- Best practice and long-term maintainability first.
- Before integrating a new library/pattern/tool, verify current best practice and latest
  stable version via Context7 (web search if needed) before writing code.
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
- Acceptance criteria met; tests green (`-race` for Go); lint and format clean.
- A code review has been run on the change and every correctness finding resolved.
- No commit, push, or merge without explicit human approval.

## Code review (mandatory, every card)
Every card carries a code-review gate and it is never optional. Run a review on the diff,
resolve all correctness findings, address or justify quality findings, then re-review after
changes. A card is not Done until its review passes.

## On-demand context (load only when relevant)
- Next.js frontend -> nextjs skill.
- Go backend -> go skill.
- Running/adding tests, finishing a card, CI checks -> testing skill.
- Terraform / AWS -> terraform skill.
- Writing/reviewing code -> coding-philosophy skill.
- Linear cards / roadmap -> roadmap-linear skill; commands `/roadmap`, `/card`.
- Implementing a card / parallel card delivery -> delivering-linear-cards skill; commands `/pick`, `/reconcile`.
- Integrating a new pattern -> research-patterns skill; command `/research`.

## Commands
`/roadmap`, `/pick`, `/card`, `/research`, `/version`, `/reconcile`, `/mayday` (router).

## State
`.factory-state.json` (gitignored) holds identity, Linear ids, and the `stack` choices.
