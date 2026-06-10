---
description: Rebase one or more card branches onto latest main, resolving conflicts per branch
---

Bring card branches up to date with `main` by rebasing each onto it. Branches are
independent: each stays its own PR, none are combined. Use the `delivering-linear-cards`
skill for the branch-naming and card conventions.

## Inputs

Resolve the set of branches to reconcile, in this order:

1. Branches the user named explicitly (by branch or by card, e.g. `VER-6`, `VER-9`).
2. Otherwise, every branch backing a worktree under `.claude/worktrees/` (and legacy
   `.worktrees/` trees still on disk).

Each branch name is `<TEAM>-<NUMBER>-<slug>`, so the `<TEAM>-<NUMBER>` prefix IS the card
binding. Use it to look up the card's intent (roadmap-linear skill / Linear MCP) when
resolving conflicts. If a branch does not match that pattern, ask which card it belongs to.

## Per branch (independently)

1. `git fetch origin`.
2. Rebase onto latest main, in the branch's worktree if one exists (`.claude/worktrees/<branch>`,
   or legacy `.worktrees/<branch>`), else by checking the branch out: `git rebase origin/main`.
3. **On conflict:** read both sides, resolve semantically using the card's intent (never blind
   "accept theirs/ours"), `git add` the resolved files, `git rebase --continue`. Stop and ask
   the user only when a conflict is genuinely ambiguous.
4. **After a clean rebase:** run the build plus the full test suite for that branch (Go:
   `go test -race ./...`; frontend: the project test command). A red or skipped test means
   stop and report that branch as failed; do not paper over it.
5. Move to the next branch.

## Report

Per branch: rebased clean / resolved N conflicts / tests green or red (with the failure). End
with a one-line summary across all branches.

## Guardrails

- **Never push.** Rebase rewrites history, so an already-pushed branch would need a
  force-push; pushing (force or otherwise) is a separate step that needs explicit human
  approval, per the workspace always-on rules.
- If a rebase is unrecoverable or the user aborts, `git rebase --abort` to leave the branch as
  it was, and report it.
- Never merge or combine branches. Each card stays independently mergeable.
