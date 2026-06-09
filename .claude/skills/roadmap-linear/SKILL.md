---
name: roadmap-linear
description: Use when creating, updating, or syncing Linear cards / the roadmap for this workspace
---

# Roadmap & Linear Rules

Identity comes from `.factory-state.json`: `linear_team_id`, `linear_project` (and its id).

## Fetching
- Fetch a card by identifier with the `issue` tool (e.g. `INF-19`). Never `search_issues`.

## Creating / updating
- `create_issue` requires both `teamId` and `projectId`.
- `update_issue` takes the issue UUID. `update_issue_state` changes state.

## Card structure
Every card MUST contain these seven sections, in order. Bold headings (not `#`), inline code
for paths/env vars. A card missing Context, Definition of Done, or Code review is incomplete.

1. **Outcome** - one opening paragraph, operator perspective.
2. **Context** - where the work sits, what it depends on and feeds, key decisions and
   constraints. This is what stops a card from being under-specified; never omit it.
3. **Approach** - the best-practice way to build it: verify versions via Context7 first,
   architecture boundaries to respect, libraries, pitfalls to avoid. State the standards
   inline; never name internal skills or workspace files (see confidentiality below).
4. **Acceptance criteria** - `*` bullets, operator perspective.
5. **Implementation todos** - granular `- [ ]` checkboxes, implementer perspective.
6. **Definition of Done** - `- [ ]` gates: versions verified, tests green (`-race` for Go),
   lint/format clean, errors wrapped, no secrets committed. Mirror `CLAUDE.md`.
7. **Code review (mandatory)** - `- [ ]` gate: run the review, resolve correctness findings,
   re-review, no merge without human approval. Required on every card; never optional.

Scale Context/Approach to the work, but they are never empty. Restate engineering standards
as requirements inside the card so the implementing agent is obliged to follow them without
reading anything else.

## Tone & confidentiality
- Short direct sentences. No emojis. No filler.
- Never mention agent files, paths, or internal workspace structure in card content.

## Version card
- A card titled `agent-industry-version` mirrors the local `VERSION` file. Keep it in `Done`.
