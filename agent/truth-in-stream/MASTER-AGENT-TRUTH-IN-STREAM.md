# MASTER-AGENT: Truth in Stream

```
CREATED: 2026-06-09
LAST_UPDATED: 2026-06-09
VERSION: 1.0.0
AGENT_INDUSTRY_VERSION: 5.0.0
SCOPE: Truth in Stream domain -- orchestrates the full agent hierarchy
LINEAR_TEAM: TBD (Linear setup handled separately)
LINEAR_PROJECT: TBD
```

## Purpose

Single entry point for any agent working on Truth in Stream. It does not contain
implementation details. It maps the domain into scoped agents and routes you to the right
place. Import this file when starting any task (code, infra, deployment, planning).

## Domain Overview

Truth in Stream is a real-time fact-checking system for live streams. Codebase:

- Frontend: Next.js 16 (App Router, React 19, TypeScript, Tailwind v4) - `stack/frontend`
- Backend: Go (stdlib net/http service) - `stack/backend`
- Infra: Terraform on AWS, region `eu-west-3`, directory-per-env - `stack/terraform`

Per-stack engineering knowledge lives in Claude skills (`nextjs`, `go`, `terraform`), loaded
on demand. This tree handles orchestration and planning, not stack details.

## Agent Hierarchy

```
master (orchestration)
└── roadmap (planning) -- backlog, dependencies, Linear card rules
```

Lean tree: application and platform work routes directly to the relevant stack skill. Add
scoped child agents here if a domain grows enough to need its own persistent context.

## Folder Structure

```
agent/truth-in-stream/
  MASTER-AGENT-TRUTH-IN-STREAM.md      <-- this file
  plans/
    ROADMAP-TRUTH-IN-STREAM.md         <-- planning + Linear card rules
```

## Child Registry

| Child | Scope | Path | update_when |
|-------|-------|------|-------------|
| roadmap | Backlog, dependency tracking, Linear card rules | `agent/truth-in-stream/plans/ROADMAP-TRUTH-IN-STREAM.md` | Any phase/state change, new Linear cards, design decisions, card-rule changes |

## Linear Card Policy

Before creating or updating any Linear card, read the roadmap agent first:

```
agent/truth-in-stream/plans/ROADMAP-TRUTH-IN-STREAM.md
```

The roadmap owns all Linear card rules (structure, formatting, tone, defaults, MCP usage,
confidentiality). No other file duplicates them. Defer to its "Linear Card Rules" section.

## Action Routing

| Task | Read / route to |
|------|-----------------|
| Frontend code (`stack/frontend`) | `nextjs` skill |
| Backend code (`stack/backend`) | `go` skill |
| Infra (`stack/terraform`) | `terraform` skill |
| Any code authoring/review | `coding-philosophy` skill |
| Planning, backlog, Linear cards | roadmap agent (above) |
| Integrating a new library/pattern | `research-patterns` skill / `/research` |

## Delegation Protocol

1. Identify task scope (which part of `stack/`, which concern).
2. For implementation, load the matching stack skill and work directly.
3. For planning or Linear, route through the roadmap agent.
4. Cross-cutting tasks: handle each affected area with its skill, then update the roadmap.

## Update Protocol

After completing any task:

1. If a roadmap phase completed or a Linear card changed, update the roadmap.
2. If the agent hierarchy changed (new/removed agents, scope changes), update this file.

## Document Maintenance

```
CREATED: 2026-06-09
LAST_UPDATED: 2026-06-09
DOCUMENT_OWNER: Truth in Stream Team
AUTHORS: [TO BE FILLED]

UPDATE_TRIGGERS:
- New child agents or sub-masters created
- Agent scope changes
- New Linear tickets added to the roadmap
- Action routing table needs new entries
- Folder structure changes
- Agent hierarchy changes
```

END_OF_DOCUMENT
