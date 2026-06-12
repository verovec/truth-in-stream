---
description: Claim the next ready card (parallel-safe) and deliver it in an isolated worktree
---

Run from a fresh session to pick up one card and deliver it. Several sessions can run `/pick`
at the same time, one card each; the claim protocol guarantees no two sessions take the same
card. Card and claim rules live in the `roadmap-linear` and `delivering-linear-cards` skills.

## Arguments
- (none): claim the top ready card and deliver it.
- `--steal <ID>`: forcibly reclaim a card stuck `In Progress` from a dead session. Skips the
  ready-queue scan and the claim race. Use only when you know the prior session is gone.

## Steps

1. **Refresh the ready queue.** Run `/roadmap` to pull live Linear states and recompute the
   Ready queue in `agent/<org_slug>/plans/ROADMAP-<ORG_UPPER>.md`.
2. **Walk the Ready queue top-down.** For each candidate card, claim it (parallel-safe):
   a. Fetch it live (`issue` tool). If its state is not `Todo`, skip it - another session took it.
   b. Generate a nonce: `openssl rand -hex 4`.
   c. Set the card to `In Progress` (`update_issue_state`), then post a comment exactly
      `CLAIM <nonce>`. Linear stamps each comment with a server `createdAt`.
   d. Sleep a jitter: `sleep $((RANDOM % 4 + 2))`.
   e. Re-fetch the card's comments. Among all `CLAIM` comments, the winner is the one with the
      earliest `createdAt` (tie-break: lexicographically smallest nonce).
      - Winner is mine -> claimed; stop the loop.
      - Winner is not mine -> I lost. Leave the card alone (the winner owns it, already
        `In Progress`); move to the next candidate.
3. **Nothing claimable** (queue empty or every ready card taken): report "no ready cards" and
   stop. Do not create a branch.
4. **On a successful claim**, hand off to `delivering-linear-cards`: create the worktree
   (`EnterWorktree`; fallback `git worktree add .claude/worktrees/<branch> -b <branch> main`) and
   run the full per-card workflow (TDD -> `/code-review` -> verify green -> PR -> In Review)
   inside it. `/code-review` runs before every PR; it is never skipped. The card is already
   `In Progress`; do not flip it again. A card reached through the Ready queue always branches
   off `main`, because the queue only surfaces cards whose dependencies are already `Done`.

5. **Optionally continue to a dependent card (stacked, separate PR).** After your PR is open and
   the card is `In Review`, you MAY - in the same session - claim a `Todo` card that depends only
   on the card you just delivered (its other dependencies, if any, already `Done`) and deliver it
   without waiting for your PR to merge. This is the ONLY time a card is taken before its
   dependency has merged to `main`. It exists so a chain of dependent cards can be delivered back
   to back with zero merge conflicts.
   a. Claim it with the same parallel-safe protocol (step 2).
   b. Base its worktree on the branch you just delivered, NOT on `main`:
      `git worktree add .claude/worktrees/<branch> -b <branch> <dependency-branch>`.
   c. Run the full per-card workflow (TDD -> `/code-review` -> verify green -> PR), opening the PR
      against the dependency branch as its base so it is a stacked PR showing only its own diff.
      When the dependency merges, GitHub retargets the PR to `main` automatically.
   d. Keep it rebased on the dependency branch while that branch changes in review; rebase it onto
      `main` once the dependency has merged.
   e. One dependent at a time: finish it fully before considering the next link in the chain.

## --steal <ID>
Skip steps 1-2. Confirm the prior session is gone, set/keep the card `In Progress`, post a
`CLAIM <nonce>` comment noting the steal, then go straight to step 4 for that card.

## Verify
Two sessions running `/pick` at the same instant must finish with exactly one CLAIM winner per
card; the loser advances to the next ready card and never reverts a state. If no card is ready,
both report "no ready cards" without branching.
