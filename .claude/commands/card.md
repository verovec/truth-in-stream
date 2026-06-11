---
description: Create or update a Linear card following the workspace card rules
---

Use the roadmap-linear skill for structure, tone, and MCP usage. Read it first.

1. Read `.factory-state.json` for `linear_team_id` and the project id.
2. Ask what the card is (feature or bug) and gather the outcome + acceptance criteria.
3. Before creating, check for file overlap with other cards. Cards run one agent each, in
   parallel worktrees, so two cards that touch the same files collide at merge. If this work
   would overlap a card that can run concurrently, do NOT create a second card - merge it into
   the right host card instead. Split overlapping work only behind a real `depends_on` ordering.
4. When updating, only ever edit a `Todo` (or unstarted `Backlog`) card. A card in any started
   state (`In Progress`, `In Review`, past `Todo`) is being processed by an agent; never edit it
   on the fly. To change started work, open a new follow-up card.
5. Create with `create_issue` (both `teamId` and `projectId`) or update with `update_issue`
   (UUID). Never mention agent files or paths in the content.
6. Create every card directly in the `Todo` state, never `Backlog` - `/pick` only claims
   `Todo` cards. Set the state explicitly at creation (or with `update_issue_state`
   immediately after) so the card is claimable as soon as it exists.
