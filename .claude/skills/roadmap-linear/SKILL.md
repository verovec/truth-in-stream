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
- Create every card directly in `Todo`, never `Backlog` - `/pick` only claims `Todo` cards.
  Set the state at creation (or flip it with `update_issue_state` right after) so the card
  enters the Ready queue immediately.
- Only `Todo` (and unstarted `Backlog`) cards may be edited. A card in any started state
  (`In Progress`, `In Review`, or anything past `Todo`) is being processed by an agent right
  now; NEVER edit its title, description, scope, or dependencies on the fly - the change races
  the running agent and corrupts its working assumptions. To change in-progress work, leave the
  card alone and open a new follow-up card instead.

## Sizing cards to avoid merge conflicts
Each card is delivered by its own agent, in parallel, in an isolated worktree. Two cards that
touch the same files collide at merge time. Size cards so concurrently-runnable cards do not
overlap on files - prefer one larger conflict-free card over several small cards that race on
the same area. Granularity is not the goal; clean parallelism is.
- Before creating a set of cards, map each candidate to the files/areas it will touch. If two
  candidates would edit the same files AND could run in parallel (neither must reach `Done`
  before the other starts), do NOT create them as separate cards. Merge them into one larger
  card scoped to that shared area, folding the smaller one's outcome, criteria, and todos into
  the right host card.
- The only legitimate way to split work that overlaps on files is a hard dependency: if B must
  not start until A is `Done`, record it in `depends_on` so they never run concurrently. Use
  this only when the ordering is real, not to dodge merging.
- This sizing decision happens at creation. Once a card leaves `Todo` it cannot be re-sliced
  (see the editing rule above), so fold any newly-found overlap into a still-`Todo` card or a
  brand-new card - never into a started one.

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

## Dependencies & the ready queue (feature tracker)
The ROADMAP state file is the source of truth for ordering, so parallel sessions can auto-pick
the right card. `/roadmap` writes three things into `agent/<org_slug>/plans/ROADMAP-<ORG_UPPER>.md`:

1. **Card list** - `| ID | Title | State | Priority | depends_on |`. `depends_on` is a
   comma-separated list of card IDs that must reach `Done` first (empty if none). Derive it from
   each card's stated dependencies (Context section); keep it in sync on every `/roadmap`.
2. **Dependency graph** - the edges as `A -> B` (A must be Done before B starts).
3. **Ready queue** - the computed pick order. A card is READY when its state is `Todo` AND every
   `depends_on` card is `Done`. Order READY cards by Linear priority, then by unblock-count (how
   many cards it transitively blocks, so critical-path work goes first), then by card number.

`/pick` reads the Ready queue top-down and claims the first card it can. Never hand-edit the
Ready queue - it is derived; fix `depends_on` or card states and re-run `/roadmap`.

A dependency only sequences `Todo` work. Two refinements at delivery time (see
`delivering-linear-cards`): a session that has just delivered a card MAY continue to a dependent
whose only unmerged dependency is that card, stacking the dependent's branch on the dependency's
branch (a single chain, not a multi-dependency card); and once both a dependency and its dependent
are `In Review`, the link is removed and the rebased branches alone guarantee a conflict-free
in-order merge. Because the Ready queue only considers `Todo` cards, removing a link from an
`In Review` card never changes it.

## Version card
- A card titled `agent-industry-version` mirrors the local `VERSION` file. Keep it in `Done`.
