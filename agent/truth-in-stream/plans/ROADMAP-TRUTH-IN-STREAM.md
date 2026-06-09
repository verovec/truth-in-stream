# Truth in Stream Roadmap - Implementation Plan

```
LINEAR_TEAM: TBD (Linear setup handled separately)
LINEAR_PROJECT: TBD
LINEAR_TICKETS: none yet
AGENT_INDUSTRY_VERSION: 5.0.0
SCOPE: Truth in Stream implementation lifecycle
STATUS: Active
CREATED: 2026-06-09
LAST_UPDATED: 2026-06-09
```

## Linear Card Rules

Single source of truth for how Linear cards are created and updated across all agents in this
domain. Every agent references this section via the roadmap gate. Do not duplicate elsewhere.

### Confidentiality

- NEVER mention agent files, their paths, their existence, or their structure in card content
- NEVER reference the `agent/` folder or any `*.md` agent file
- NEVER describe how agent documentation is organized or where it lives
- Cards are for humans and must contain only actionable implementation content

### Card Structure

Every card description follows this exact layout:

1. **Opening paragraph** - 1-3 sentences explaining what the card is about. Plain language. Say what happens, not how it works internally
2. **Acceptance Criteria** (`**Acceptance Criteria**`) - bullet list of observable outcomes. Each item starts with `*` and describes a verifiable result, not an implementation step
3. **Todo** (`**Todo**`) - checkbox list of concrete implementation tasks. Each item starts with `- [ ]` and is a single actionable step

### Formatting

- Use markdown bold (`**text**`) for section headings, not `#` headings
- `*` bullets for acceptance criteria, `- [ ]` checkboxes for todos
- Inline code (backticks) for command names, file paths, resource names, env vars
- Tables only for small side-by-side comparisons. Do not overuse
- No emojis. No filler ("In order to...", "This ticket aims to..."). Get to the point
- Single hyphen `-` as separator, never double dash
- Do not end a sentence with a period if it is the only/last sentence of a paragraph

### Tone

- Short, direct sentences. Technical but not dense
- Acceptance criteria from the user's/operator's perspective; todos from the implementer's

### Defaults

- **State**: Always create cards in "Todo", never "Backlog"
- **Cross-references**: Use the identifier directly (e.g. "see INF-22")
- **Scope awareness**: Align card content with the current roadmap phase; avoid duplication

### MCP Tool Usage

Use the Linear MCP server (the server whose tools include `get_viewer`, `projects`,
`project_issues`, `create_issue`, etc.):

- **Create**: `create_issue` with `teamId` (use `get_viewer` to list teams if needed)
- **Update description**: `update_issue` with the issue UUID (fetch it first with `issue` using the identifier)
- **Change state**: `update_issue_state` with `stateId` (fetch with `issue_states`)
- **Fetch by identifier**: use the `issue` tool with the identifier string; do NOT use `search_issues` for identifier lookups

## Dependency Graph

```
(no phases defined yet)
```

## Current State (as of 2026-06-09)

Project scaffolded via /setup. Runnable skeletons in place:

- Frontend: Next.js 16 app (`stack/frontend`), Vitest configured, lint + test green
- Backend: Go stdlib HTTP service with `/healthz` and graceful shutdown (`stack/backend`), vet + test green
- Infra: Terraform dev/prod root modules (`stack/terraform`), S3 native-locking backend, fmt + validate green
- CI: pr (lint+test), terraform (fmt/validate/plan, apply dev on main), deploy (build+push images to GHCR)
- Local dev: docker-compose (frontend :3000, backend :8080)

Linear project linking is being handled separately - update the LINEAR_* fields above and in
`.factory-state.json` once available.

Open follow-ups (not yet ticketed):
- Bootstrap the `truth-in-stream-tfstate` S3 bucket (versioning + encryption) before first apply
- Create the `AWS_ROLE_ARN` IAM role (GitHub OIDC) for CI Terraform/deploy
- Define the AWS runtime target (ECS / App Runner) and wire the deploy.yml AWS step
- Upgrade local Go toolchain (1.20 -> 1.26) to unlock slog + ServeMux method routing

---

## Design Decisions Log

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Terraform state locking | Native S3 `use_lockfile` | GA since TF 1.11; DynamoDB lock table deprecated, fewer resources |
| 2 | Terraform layout | Directory-per-env (dev/prod) | True state isolation, no cross-env blast radius |
| 3 | Frontend test runner | Vitest | Handles ESM + async Server Components; Jest does not |
| 4 | Backend framework | None (stdlib net/http) | ServeMux method routing (1.22+) covers the need; fewer deps |

---

## Document Maintenance

```
CREATED: 2026-06-09
LAST_UPDATED: 2026-06-09
DOCUMENT_OWNER: Truth in Stream Team
AUTHORS: [TO BE FILLED]

UPDATE_TRIGGERS:
- Any phase completion or state change
- New Linear cards created that affect this roadmap
- Design decisions finalized
- Implementation details changed during execution
- Linear card rules or formatting standards change
```

END_OF_DOCUMENT
