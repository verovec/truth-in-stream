# truth-in-stream

Real-time fact-checking for live streams. The workspace is the monorepo; project code
lives under `stack/`.

## Stack
- Frontend: Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) - `stack/frontend`
- Backend: Go (stdlib net/http service) - `stack/backend`
- Infra: Terraform on AWS, region `eu-west-3`, directory-per-env - `stack/terraform`
- Local dev: `docker-compose.yml` (backend :8080, frontend :3000)

## Always-on rules
- Never commit, push, or deploy on your own. Always ask for explicit approval first.
- Best practice and long-term maintainability first.
- Before integrating a new library/pattern/tool, verify current best practice and latest
  stable version via Context7 (web search if needed) before writing code.
- Never use emojis. No comments that restate code. Only touch files needed for the task.

## On-demand context (load only when relevant)
- Next.js frontend -> nextjs skill.
- Go backend -> go skill.
- Terraform / AWS -> terraform skill.
- Writing/reviewing code -> coding-philosophy skill.
- Linear cards / roadmap -> roadmap-linear skill; commands `/roadmap`, `/card`.
- Integrating a new pattern -> research-patterns skill; command `/research`.

## Commands
`/roadmap`, `/card`, `/research`, `/version`, `/mayday` (router).

## State
`.factory-state.json` (gitignored) holds identity, Linear ids, and the `stack` choices.
