---
description: Claim the next eligible local card (parallel-safe), deliver it in an isolated worktree, then chain to the next
---

Run from a fresh session to work the local card queue. Several sessions can run `/pick` at the
same time: the claim is atomic, so each session gets a different card. Use the `local-cards`
skill for the queue, the statuses and the claim, and the `delivering-linear-cards` skill for the
per-card workflow. Read both first.

## Arguments
- (none): claim the next eligible card, deliver it, then keep chaining.
- `<ID>` (e.g. `/pick G2`): claim that card instead of the next one. Refused if it is not
  `todo` or a dependency is not `done`.
- `<epic>` (e.g. `/pick G`): chain only through the cards of that epic.
- `--once`: deliver one card and stop instead of chaining.
- `--steal <ID>`: take over a card held by a session the operator confirms is gone.

## Steps

1. **Claim.** `scripts/cards.sh pick --by "<session id or branch>"` (add `--epic <E>` for an
   epic run, or use `claim <ID>` for a named card). The printed id is the card you own. Exit
   code 3 means nothing is eligible: report what the script listed and stop, without creating a
   branch.
2. **Load the card.** `scripts/cards.sh path <ID>`, read the card, and read the `next:` Journal
   line of each card it depends on.
3. **Isolate.** Create a worktree and the branch named in the card's frontmatter off latest
   `origin/dev`.
4. **Deliver.** Run the per-card workflow of `delivering-linear-cards`:
   `TDD -> /code-review -> unit and end-to-end checks green -> rebase on dev -> PR against dev`.
   `/code-review` is never skipped. Tick the card's todos in its file as they are done.
5. **In review.** When the PR is open: `scripts/cards.sh set <ID> in-review "PR #<n>"`.
6. **Merge and release.** When every CI check is green, merge the PR to `dev`, then
   `scripts/cards.sh set <ID> done "PR #<n> · next: <what the next card must know>"` and tick
   the card's acceptance criteria. Red CI never merges: fix it and push again.
7. **Chain.** Clean up the worktree and go back to step 1, unless `--once` was given. Deliver
   one card fully, through the merge, before claiming the next.

## When the chain stops

Stop and report, without claiming another card, when nothing is eligible, when a card is set
`blocked`, when CI is red twice in a row on the same card, or when a card needs something only
the operator can provide. The report lists the cards merged with their PRs, the card that
stopped the chain and why, and what the operator must decide or provide.

## --steal <ID>

Skip step 1. Confirm with the operator that the previous session is gone, run
`scripts/cards.sh steal <ID> --by "<session>"`, then continue from step 2, reusing the card's
existing branch and PR when there are any.

## Verify

Two sessions running `/pick` at the same instant end with two different cards, or one card and
one "nothing eligible". A card whose dependency is not `done` is never started. A card ends
`done` only after its PR merged to `dev` on green CI. `/pick` never merges to `main`, never
tags a release, and never triggers a deploy.
