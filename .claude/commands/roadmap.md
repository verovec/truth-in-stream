---
description: Sync the roadmap state file from Linear for this workspace
---

Use the roadmap-linear skill for all card rules.

1. Read `.factory-state.json` for `linear_team_id` and `linear_project`.
2. List the project's issues via the Linear MCP (by project id).
3. Write a lean state file at `agent/<org_slug>/plans/ROADMAP-<ORG_UPPER>.md` with these three
   sections and no rules text (rules live in the roadmap-linear skill):
   - **Card list**: `| ID | Title | State | Priority | depends_on |` (one row per card).
   - **Dependency graph**: the `depends_on` edges as `A -> B` lines.
   - **Ready queue**: the computed pick order - cards whose state is `Todo` and whose every
     `depends_on` card is `Done`, ordered by priority, then unblock-count, then card number.
     This is what `/pick` consumes; see the roadmap-linear skill for the exact rule.
4. Report the counts (e.g. "3 Todo, 1 In Progress, 2 Done") and the size of the Ready queue.
