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
4. **On a successful claim**, hand off to `delivering-linear-cards`: create the worktree and run
   the full per-card workflow (TDD -> `/code-review` -> verify green -> PR -> In Review) inside it.
   `/code-review` runs before every PR; it is never skipped. The card is already `In Progress`; do
   not flip it again. **Choose the branch base from the card's dependencies** (the Ready queue
   surfaces both shapes):
   - **Every `depends_on` card is `Done` -> branch off `main`**; the PR targets `main`.
     `EnterWorktree` (fallback `git worktree add .claude/worktrees/<branch> -b <branch> main`).
   - **Exactly one `depends_on` card is `In Review` (the rest `Done`) -> stack on that dependency's
     branch.** Resolve `<dependency-branch>` from the dependency's worktree/branch matching its
     `<TEAM>-<NUMBER>-` prefix (or from its open PR), then
     `git worktree add .claude/worktrees/<branch> -b <branch> <dependency-branch>` and open the PR
     against `<dependency-branch>` so it is a stacked PR showing only its own diff. GitHub
     retargets it to `main` when the dependency merges. Keep it rebased on the dependency branch
     while that branch changes in review; rebase onto `main` once the dependency has merged. Once
     this card is also `In Review`, remove the Linear `depends_on` link (per `delivering-linear-cards`).

5. **Optionally continue in the same session.** After your PR is open and the card is `In Review`,
   you MAY loop back to step 2 and claim the next ready card rather than ending the session. A card
   that depends only on the one you just delivered is now READY (its dependency is `In Review`), so
   the queue surfaces it and step 4 bases it on the dependency branch as a stacked PR. Continuing in
   the same session is the fast path for a dependent chain, but that card is equally claimable by a
   fresh `/pick` session - the Ready queue, not the session, is what unblocks it. Deliver one link
   fully before starting the next.

## --steal <ID>
Skip steps 1-2. Confirm the prior session is gone, set/keep the card `In Progress`, post a
`CLAIM <nonce>` comment noting the steal, then go straight to step 4 for that card.

## Verify
Two sessions running `/pick` at the same instant must finish with exactly one CLAIM winner per
card; the loser advances to the next ready card and never reverts a state. If no card is ready,
both report "no ready cards" without branching.
