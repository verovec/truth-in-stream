---
name: delivering-linear-cards
description: Use when implementing a Linear card or feature, when opening a PR for tracked work, or when asked to update Linear cards/status/checkboxes. Defines what you own (execute through open PR) versus what the human owns (merge).
---

# Delivering Linear Cards

## Overview

You own the work from card to open PR. The human owns the merge. When asked to "update Linear," just do it: flip status, check boxes, add a one-line comment. No option menus, no "want me to..?". Act.

## Responsibility split

- **You:** enrich the card if thin, branch, implement with tests, run `/code-review` and apply it, verify the full suite is green, check off the card's todos, open the PR, set status to In Review, link the PR.
- **The human:** reviews and merges. Whether or when it merges is not yours to track, poll, or gate on. When told it merged (or asked to update), set the card to Done.

## Workflow (per card)

1. **Card must be executor-ready.** A different agent authors the card than executes it, so it must stand alone: outcome, context, approach, acceptance criteria, todos, definition of done. If the card you are handed is thin, enrich it first (format: `roadmap-linear` skill). Do not execute a vague card.
2. **Claim the card before any work (parallel-safe).** `In Progress` is the lock. Set it the instant you take the card, before branching or writing a line, so a parallel session sees the card is taken. When you reach the card through `/pick`, the claim has already run; when you take a card by hand, claim it yourself with the protocol below.
3. **Branch off `main` (or off a dependency branch).** Never implement on `main`. Branch off `main` by default. The one exception: when you are continuing to a card that depends on another card already delivered but NOT yet merged to `main`, branch off that dependency's branch instead, so your work stacks on it and you never replay its conflicts (see "Continue to a dependent card" below). The branch name MUST be `<TEAM>-<NUMBER>-<slug>`: the card's team identifier and number exactly as Linear shows them (uppercase, e.g. `VER-6`), a single dash, then a slug of 3 to 5 lowercase words joined by underscores. NEVER add a username/author prefix, NEVER use dashes inside the slug, NEVER exceed 5 words. Distill the card title to its essence; do not transcribe it verbatim. Example: card `VER-6 "Curated verification database schema and ingestion (pgvector)"` -> `VER-6-pgvector_database_schema`.

   **Worktree isolation.** Mandatory whenever cards run in parallel (always the case via `/pick`); opt-in for a single card taken by hand. Give each card its own worktree so cards never share a working tree: prefer the native worktree tool (`EnterWorktree`), and fall back to `git worktree add .claude/worktrees/<branch> -b <branch> main` only if no native tool exists. Worktrees live under the project-local `.claude/worktrees/` directory (gitignored; `.worktrees/` is also ignored for legacy trees already on disk); the worktree directory name is the branch name, so it doubles as the card binding. The branch inside MUST still follow the `<TEAM>-<NUMBER>-<slug>` rule. Everything else in this workflow (TDD, `/code-review`, verify green, PR) runs unchanged inside each worktree. A card with no unmerged dependency is an independent branch off `main` whose PR targets `main`; a stacked dependent card is based on its dependency's branch and its PR targets that branch (GitHub retargets it to `main` when the dependency merges). Keep every branch rebased onto its base: `main` for an independent card, the dependency branch for a stacked one (and onto `main` once that dependency has merged). `/reconcile` rebases onto `main`, so use it for independent branches; rebase a stacked branch onto its dependency branch by hand until the dependency lands.
4. **TDD with regression safety.** Write tests first (REQUIRED: superpowers:test-driven-development). Tests must prove the new behavior AND guard existing behavior, so a merge cannot silently break something else. The WHOLE suite must pass, not just the new test.
5. **Run `/code-review`, then apply it.** Before pushing, run `/code-review` and apply the findings. Re-run tests afterward.
6. **Verify green, never push broken code.** Build plus the full test suite pass locally (REQUIRED: superpowers:verification-before-completion). A failing build or a red or skipped test means do not push.
7. **Check the card's boxes** you completed (edit the description, `- [ ]` to `- [X]`).
8. **Open the PR** with a summary and a test plan that references the card. The feature-branch push and PR are the delivery hand-off; that part does not need separate approval.
9. **Set the card to In Review** and link the PR in a comment.
10. **Stop, or continue to a dependent card.** The human merges; do not poll PR state. After the
    PR is open you MAY continue to a card that depends on the one you just delivered (see below),
    instead of ending the session.

## Continue to a dependent card (optional, stacked)

A chain of dependent cards is delivered back to back so the chain never produces a merge
conflict: each link is built on the previous link's branch rather than waiting for it to merge.

- **When.** Only after the current card's PR is open and it is `In Review`. Only take a card whose
  remaining dependency is the card you just delivered (its other dependencies already `Done`).
  This is the single case where a card is worked before its dependency has merged to `main`.
- **Claim** it with the same parallel-safe protocol as any card (`In Progress` + `CLAIM` nonce).
- **Base the worktree on the dependency branch, not `main`:**
  `git worktree add .claude/worktrees/<branch> -b <branch> <dependency-branch>` (branch name still
  `<TEAM>-<NUMBER>-<slug>`).
- **Deliver it fully** - TDD, `/code-review` (never skipped), verify green - then **open its PR
  against the dependency branch** as the base. It is a stacked PR and shows only its own diff;
  GitHub retargets it to `main` when the dependency merges.
- **Keep it rebased on the dependency branch** while that branch changes in review; rebase onto
  `main` once the dependency has merged.
- **One link at a time.** Finish a dependent fully before starting the next link in the chain.

### When both are in review, drop the link and keep them mergeable

Once the dependency and its dependent are both `In Review` (both PRs open), the Linear dependency
link has done its only job - sequencing the work - and now just gates the merge. Remove it and let
the rebased branches carry correctness:

- **Remove the Linear dependency** between the two cards (`removeBlockedBy` on the dependent). Do
  this only after both PRs are open; never on a card still `Todo` or `In Progress`, where the link
  still drives the Ready queue.
- **Rebase for a clean in-order merge.** Keep the dependent rebased on the dependency branch (then
  on `main` once the dependency lands) so that merging in dependency-first order is always
  conflict-free. The branches, not the Linear link, are what prevent conflicts.
- **Merge order stays the human's call,** and it is dependency-first: merge the dependency PR, then
  the dependent (which GitHub has retargeted to `main`). Because the dependent was rebased on the
  dependency, that second merge is a fast-forward with nothing to resolve.

## Claiming a card (parallel-safe)

Independent sessions run in parallel, one per card, and share a single Linear identity (the
assignee cannot tell sessions apart). The lock is the `In Progress` state plus a nonce comment,
ordered by Linear's server-side timestamp. `/pick` runs this for you; do it by hand only when
taking a card outside `/pick`.

1. Confirm the card is still `Todo` (fetch it live). If it is already `In Progress`, another
   session owns it - pick a different card.
2. Generate a nonce: `openssl rand -hex 4`.
3. Set the card to `In Progress`, then post a comment exactly `CLAIM <nonce>`. Linear stamps
   each comment with a server `createdAt`.
4. Sleep a short jitter (`sleep $((RANDOM % 4 + 2))`), then re-fetch the card's comments.
5. Among all `CLAIM` comments, the **winner is the one with the earliest `createdAt`** (tie-break:
   lexicographically smallest nonce). Using the server timestamp - never a client clock - is what
   makes this race-safe.
   - Your nonce wins -> the card is yours; proceed to branch and deliver.
   - Your nonce loses -> the winner already holds it (correctly `In Progress`); do NOT revert the
     state, just move to the next card.

To reclaim a card stuck `In Progress` after a session died, use `/pick --steal <ID>`.

## Status transitions

| Event | Card status |
|---|---|
| You claim the card (before any work) | In Progress |
| PR opened | In Review |
| Human says merged, or asks you to update | Done (check any remaining boxes) |
| Work blocked on an external dependency (e.g. cloud infra) | leave In Progress; note the blocker in a comment |

## Red flags, stop

- Starting work on a card still in `Todo`: claim it (`In Progress` + `CLAIM` nonce) first, or a parallel session will collide with you.
- Lost a claim race and about to revert the card to `Todo`: don't. The winner owns it; move on.
- About to push without running `/code-review`: run it and apply it first.
- About to push with a failing build or a red or skipped test: fix first. Never push broken code.
- Handed (or writing) a thin card for another agent to execute: enrich it first.
- Asked to "update Linear" and you are drafting a paragraph of options: just make the update.
- Watching or polling whether the PR merged: not your job. Stop.

## Don't

- Don't merge or deploy on your own; those stay the human's call. (Opening the PR is yours.)
- Don't mark a card Done on PR-open; Done is after merge.
- Don't ship only a happy-path test; include regression coverage.
