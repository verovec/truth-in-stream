---
name: delivering-linear-cards
description: Use when implementing a Linear card or feature, when opening a PR for tracked work, or when asked to update Linear cards/status/checkboxes. Defines the autonomous delivery flow - execute, e2e-verify, open the PR, auto-merge on green CI, mark Done - and the one action (production deploy) the human still gates.
---

# Delivering Linear Cards

## Overview

You own the work end to end: card -> implementation -> e2e verification -> PR -> auto-merge on green CI -> Done. The only thing the human still gates is a production deploy. When asked to "update Linear," just do it: flip status, check boxes, add a one-line comment. No option menus, no "want me to..?". Act.

## Responsibility split

- **You:** enrich the card if thin, branch, implement with tests, run `/code-review` and apply it, verify the full suite is green AND an end-to-end check of the feature passes, check off the card's todos, rebase on `main`, open the PR (status In Review), wait for the PR's CI to go green, merge the PR to `main`, then set the card to Done.
- **The human:** gates production deploys only (`terraform apply`, dispatching the deploy workflow). They do not need to approve the merge; the green-CI gate stands in for review. If CI is red, you do not merge - you fix and re-push, or leave the card In Review and report.

## Workflow (per card)

1. **Card must be executor-ready.** A different agent authors the card than executes it, so it must stand alone: outcome, context, approach, acceptance criteria, todos, definition of done. If the card you are handed is thin, enrich it first (format: `roadmap-linear` skill). Do not execute a vague card.
2. **Claim the card before any work (parallel-safe).** `In Progress` is the lock. Set it the instant you take the card, before branching or writing a line, so a parallel session sees the card is taken. When you reach the card through `/pick`, the claim has already run; when you take a card by hand, claim it yourself with the protocol below.
3. **Branch off `main` (or off a dependency branch).** Never implement on `main`. Branch off `main` by default. The one exception: when you take a card whose dependency is already delivered (its PR open, `In Review`) but NOT yet merged to `main`, branch off that dependency's branch instead, so your work stacks on it and you never replay its conflicts (see "Continue to a dependent card" below). The branch name MUST be `<TEAM>-<NUMBER>-<slug>`: the card's team identifier and number exactly as Linear shows them (uppercase, e.g. `VER-6`), a single dash, then a slug of 3 to 5 lowercase words joined by underscores. NEVER add a username/author prefix, NEVER use dashes inside the slug, NEVER exceed 5 words. Distill the card title to its essence; do not transcribe it verbatim. Example: card `VER-6 "Curated verification database schema and ingestion (pgvector)"` -> `VER-6-pgvector_database_schema`.

   **Worktree isolation.** Mandatory whenever cards run in parallel (always the case via `/pick`); opt-in for a single card taken by hand. Give each card its own worktree so cards never share a working tree: prefer the native worktree tool (`EnterWorktree`), and fall back to `git worktree add .claude/worktrees/<branch> -b <branch> main` only if no native tool exists. Worktrees live under the project-local `.claude/worktrees/` directory (gitignored; `.worktrees/` is also ignored for legacy trees already on disk); the worktree directory name is the branch name, so it doubles as the card binding. The branch inside MUST still follow the `<TEAM>-<NUMBER>-<slug>` rule. Everything else in this workflow (TDD, `/code-review`, verify green, PR) runs unchanged inside each worktree. A card with no unmerged dependency is an independent branch off `main` whose PR targets `main`; a stacked dependent card is based on its dependency's branch and its PR targets that branch (GitHub retargets it to `main` when the dependency merges). Keep every branch rebased onto its base: `main` for an independent card, the dependency branch for a stacked one (and onto `main` once that dependency has merged). `/reconcile` rebases onto `main`, so use it for independent branches; rebase a stacked branch onto its dependency branch by hand until the dependency lands.
4. **TDD with regression safety.** Write tests first (REQUIRED: superpowers:test-driven-development). Tests must prove the new behavior AND guard existing behavior, so a merge cannot silently break something else. The WHOLE suite must pass, not just the new test.
5. **Run `/code-review`, then apply it.** Before pushing, run `/code-review` and apply the findings. Re-run tests afterward.
6. **Verify green, never push broken code.** Build plus the full test suite pass locally (REQUIRED: superpowers:verification-before-completion). A failing build or a red or skipped test means do not push.
7. **End-to-end check of the feature (division of labor).** The agent owns the **unit gate**: `go test -race ./...` with no live infra green, plus `gofmt`/`gofumpt`, `go vet`, `golangci-lint`. Where a behaviour needs a live broker/DB to prove, the agent **writes** the integration test (gated on `TEST_RABBITMQ_URL`/`TEST_DATABASE_URL` so it compiles and skips cleanly) but does **not** stand up Docker/rabbitmq/postgres to run it during delivery - the **live functional/e2e run is the user's responsibility**. Do not block a PR on booting a live broker or database; note the human-run functional check in the PR test plan. Only drive the running stack yourself when the card's behaviour is fully exercisable without spinning up live infra (e.g. an HTTP handler, a pure pipeline) - then do so. (This overrides the older "unit-green is not enough, run the e2e yourself" stance for live-infra-dependent cards in this workspace.)
8. **Check the card's boxes** you completed (edit the description, `- [ ]` to `- [X]`).
9. **Rebase on `main`, then open the PR.** Rebase the branch onto latest `origin/main` (independent card) or its dependency branch (stacked card) so the merge is conflict-free, then open the PR with a summary and a test plan that references the card and the e2e evidence. Set the card to In Review and link the PR in a comment. The push and PR are the delivery hand-off; they do not need separate approval.
10. **Auto-merge on green CI, then Done.** See "Auto-merge on green CI" below: watch the PR's checks, merge to `main` when they pass, and move the card to Done. Then stop, or continue to a dependent card (see below).
11. **Post the epic recap and check docs if this was the epic's last card.** After the card is Done, check whether it completed its parent epic; if so, post the epic digest and run the documentation check (see "Epic close-out digest" below).

## Auto-merge on green CI

Once the PR is open, the merge is automatic and gated only on the PR's CI. No human approval for the merge; the green-CI gate replaces it. Only a production deploy stays human-gated, and the deploy workflow is `workflow_dispatch`-only so an auto-merge never deploys.

1. **Wait for the PR's checks.** Watch them to completion: `gh pr checks <pr> --watch --fail-fast`. The PR CI is `frontend-lint`, `frontend-test`, `backend-lint`, `backend-test` (plus a terraform plan when `stack/terraform/**` changed). Do not poll in a tight busy-loop; let `--watch` block.
2. **Green -> merge.** When every required check passes, merge to the PR's base: `gh pr merge <pr> --merge --delete-branch` (an independent card's PR targets `main`; a stacked card's PR targets its dependency branch and merges there - it reaches `main` only after the dependency does, preserving dependency-first order). Confirm the merge landed (`gh pr view <pr> --json state,mergedAt`).
3. **Merged to `main` -> Done.** Once the change is on `main`, set the card to Done and check any remaining boxes. For a stacked PR not yet retargeted to `main`, the card moves to Done only when its change actually reaches `main` (after the dependency merges and the PR fast-forwards); until then leave it In Review.
4. **Red CI -> do not merge.** If any check fails, the merge does not fire. Fix the failure and push again (CI re-runs), or if it cannot be fixed now leave the card In Review and report the failing check. Never merge over a red or pending check; never override a failing required check.
5. **Clean up the worktree** after the branch merges and is deleted (`ExitWorktree`, or remove `.claude/worktrees/<branch>`).

This requires the harness to allow `gh pr merge` and `gh pr checks` for this repo (a one-time user-granted permission; an agent cannot grant itself merge rights). Without it the merge step prompts the user - an acceptable manual fallback, but the flow assumes the rules are in place. See the `/pick` command's "One-time permission" note for the exact rules.

## Epic close-out digest

When the card you just moved to Done has a parent epic, check whether that completes the epic and,
if so, post its recap. This is the automatic trigger for the epic digest - it rides the merge-to-Done
step you already perform, so there is no separate cron.

1. **Find the parent.** Fetch the card's parent issue. No parent -> skip; nothing to do.
2. **Is the epic complete?** List the epic's children with their states. The epic is complete when
   every child is `Done` or `Canceled`. If any child is still open, skip - a later delivery will be
   the one that closes it.
3. **Post the recap once.** `make digest EPIC=<epic-id>` (e.g. `make digest EPIC=VER-93`). It posts
   the epic's shipped cards (each with a one-line description) and the project's remaining work to
   Slack. Needs `SLACK_DIGEST_WEBHOOK_URL`; with `DIGEST_SUMMARY_API_KEY` set each card gets a
   synthesized description, otherwise it falls back to the card titles. If the webhook is unset, run
   `make digest EPIC=<epic-id> MODE=terminal` and paste the recap into a closing comment instead.
4. **Exactly one session posts it.** The all-children-Done check is true for only the delivery that
   lands the final card, so parallel sessions do not double-post. If unsure whether it already ran,
   confirm no prior recap exists before posting.
5. **Check the docs once, here.** Epic close-out is the only documentation-sync trigger - it rides
   this step, never individual cards. Consult the `maintaining-documentation` skill: run its
   decision gate and, if it passes, update the README and `docs/` to match what the epic shipped, or
   open a follow-up card scoped to that doc update. If the gate fails, record "no doc change needed"
   and move on.

## Continue to a dependent card (optional, stacked)

A chain of dependent cards is delivered back to back so the chain never produces a merge
conflict: each link is built on the previous link's branch rather than waiting for it to merge.
Once a dependency's PR is open (`In Review`), its dependents are READY (`roadmap-linear`) and any
session can deliver them stacked - `/pick` surfaces them from the Ready queue. Continuing in the
same session you just delivered the dependency from is the fast path, not the only one; a fresh
`/pick` claims the same card just as well.

- **When.** As soon as the dependency's PR is open and it is `In Review` - no waiting for it to
  merge. Take a card whose dependencies are all cleared with at most one still unmerged: every
  `depends_on` card is `Done` except (optionally) one that is `In Review`, and that single
  `In Review` card is the branch you stack on. A card with two or more unmerged dependencies waits
  until all but one are `Done` - you can only stack on one branch.
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
- **Merge order is dependency-first and automatic.** Each PR auto-merges when its own CI is green
  (no human gate). The dependency PR merges to `main` first; GitHub retargets the dependent to
  `main`, its CI re-runs, and because it was rebased on the dependency that second merge is a
  fast-forward with nothing to resolve. In practice the dependency usually auto-merges to `main`
  before you finish the dependent, so the dependent is simply an independent branch off `main` by
  the time you open its PR.

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
| PR opened (after rebase + e2e green) | In Review |
| PR's CI green and PR merged to `main` | Done (check any remaining boxes) |
| PR's CI red | stay In Review; fix and re-push, never merge on red |
| Work blocked on an external dependency (e.g. cloud infra) | leave In Progress; note the blocker in a comment |

## Red flags, stop

- Starting work on a card still in `Todo`: claim it (`In Progress` + `CLAIM` nonce) first, or a parallel session will collide with you.
- Lost a claim race and about to revert the card to `Todo`: don't. The winner owns it; move on.
- About to push without running `/code-review`: run it and apply it first.
- About to push with a failing build or a red or skipped test: fix first. Never push broken code.
- About to open a PR on a live-infra-dependent card: ensure the unit gate is green and the integration test is written and skips cleanly; the live functional run is the user's. Do NOT boot Docker/rabbitmq/postgres to run it yourself. For cards exercisable without live infra, still drive the real path.
- About to merge with a red or still-pending check: don't. The merge fires only on all-green CI.
- About to mark a card Done before the change is on `main`: don't. Done is after the merge lands.
- Handed (or writing) a thin card for another agent to execute: enrich it first.
- Asked to "update Linear" and you are drafting a paragraph of options: just make the update.

## Don't

- Don't merge on red or pending CI, and don't override a failing required check. The green-CI gate is the only thing standing in for human review.
- Don't deploy on your own: `terraform apply` and dispatching the deploy workflow stay the human's call.
- Don't mark a card Done before its change is on `main`; Done is after the merge lands.
- Don't ship only a happy-path test; include regression coverage and an e2e check of the feature.
