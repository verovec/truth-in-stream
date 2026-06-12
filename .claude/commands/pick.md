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
   the full per-card workflow inside it:
   `TDD -> /code-review -> verify green (unit + end-to-end) -> rebase on main -> PR -> CI green -> merge -> Done`.
   `/code-review` runs before every PR; it is never skipped. Before opening the PR, run an
   end-to-end check that exercises the feature the card adds (not just unit tests) and confirm it
   passes; a failing or absent e2e check means do not open the PR. The card is already
   `In Progress`; do not flip it again. After the PR is open and its CI is green, the merge to
   `main` and the move to Done are automatic (see `delivering-linear-cards`, "Auto-merge on green
   CI"); only a production deploy stays human-gated. **Choose the branch base from the card's
   dependencies** (the Ready queue surfaces both shapes):
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

5. **Optionally continue in the same session.** After the card has auto-merged to `main` and moved
   to Done, you MAY loop back to step 2 and claim the next ready card rather than ending the session.
   Because the card you just delivered auto-merges on green CI, a card that depended only on it is
   normally branch-off-`main` (its dependency is now `Done`), not stacked. If CI is still running and
   the dependency is only `In Review`, the dependent is still READY and step 4 stacks it on the
   dependency branch instead. Re-run `/roadmap` first so the Ready queue reflects the merge. Either
   way the next card is equally claimable by a fresh `/pick` session - the Ready queue, not the
   session, is what unblocks it. Deliver one card fully (through merge) before starting the next.

## --steal <ID>
Skip steps 1-2. Confirm the prior session is gone, set/keep the card `In Progress`, post a
`CLAIM <nonce>` comment noting the steal, then go straight to step 4 for that card.

## Verify
Two sessions running `/pick` at the same instant must finish with exactly one CLAIM winner per
card; the loser advances to the next ready card and never reverts a state. If no card is ready,
both report "no ready cards" without branching. A claimed card ends `Done` only after its PR's CI
is green and the PR has merged to `main`; if CI fails the card stays `In Review` (never Done) and
the failure is reported and fixed - the merge never fires on red CI. A production deploy is never
triggered by `/pick`.

## One-time permission
Auto-merge needs the harness to allow `gh pr merge` and `gh pr checks` for this repo. Add them once
via `/permissions` or this repo's `.claude/settings.json` (an agent cannot grant itself merge
rights). Until they are allowed, the merge step prompts you - which is a fine manual gate, but the
flow above assumes the rules are in place:

```json
"permissions": { "allow": ["Bash(gh pr merge:*)", "Bash(gh pr checks:*)"] }
```
