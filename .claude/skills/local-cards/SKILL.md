---
name: local-cards
description: Use when picking, claiming, creating, or updating a card, when asked for the roadmap or the next card, or when running /pick, /card or /roadmap. Cards and their queue live in local Markdown files, not in Linear. Defines the queue, the statuses, the parallel-safe claim, and how a session chains cards in dependency order.
---

# Local cards

Cards are tracked in local Markdown files. Linear is not used for new work: do not create,
claim, or update a card there. The card format and the sizing rules of `roadmap-linear` still
apply (seven sections, cards sized so parallel work does not collide); the delivery workflow of
`delivering-linear-cards` still applies (TDD, code review, end-to-end check, PR against `dev`,
merge on green CI). This skill replaces only where a card lives and how its status changes.

## Where things live

| Path | Content |
|---|---|
| `cards/ROADMAP.md` | **Queue** (one row per card; row order is priority) and **Journal** (one line per status change, newest first) |
| `cards/<ID>-<slug>.md` | one card: frontmatter (`id`, `title`, `epic`, `status`, `depends_on`, `branch`) then the seven sections |
| `scripts/cards.sh` | the only tool that changes a status |

`cards/` sits at the root of the MAIN checkout and is gitignored. Every session on this machine
shares it, whichever worktree it runs in: `scripts/cards.sh` resolves the directory from the
git common dir, so run it from anywhere inside the repository. Never copy `cards/` into a
worktree and never commit it.

The Queue is authoritative. If a card's frontmatter and the Queue disagree, the Queue wins.

## Statuses

`todo -> claimed -> in-review -> done`, plus `blocked` (the worker hit a wall; the reason is in
the Journal) and `dropped` (cancelled).

- `claimed`: a session owns the card and is working on it.
- `in-review`: the PR is open; code review and CI are running.
- `done`: the PR is merged to `dev`.

Every status change is one `scripts/cards.sh` call. It rewrites the Queue row, the card's
frontmatter and the Journal together. Never edit a status by hand.

The script refuses a transition that skips a step: a card reaches `in-review`, `done` or
`blocked` only from `claimed` or `in-review`, so a card can never be marked `done` without
having been claimed. A `done` card is final. The script does not record who holds a card beyond
the Journal line, so only change the status of a card your own session claimed.

## Pick rule

A card is eligible when its status is `todo` and every card in its `Depends on` column is
`done`. The next card is the topmost eligible row. Row order is the priority the operator set:
never reorder the Queue, never skip ahead of an eligible card.

A dependency that is only `in-review` does not unblock its dependent. A chain is delivered link
by link, each one merged before the next starts, so a dependent always branches off a `dev`
that already contains its dependency.

## Claiming (parallel-safe)

```
scripts/cards.sh pick                # claim the next eligible card, print its id
scripts/cards.sh pick --epic G       # same, restricted to one epic
scripts/cards.sh claim G2            # claim a named card
```

`pick` finds the next eligible card and claims it inside one locked section. Several sessions
may run it at the same instant: each gets a different card, and a session that finds nothing
gets exit code 3 with the list of what blocks. Always claim through the script before creating
a branch or writing code. Pass `--by "<session or branch>"` so the Journal says who holds it.

Exit code 3 means nothing is eligible. Report what the script printed and stop; do not wait in
a loop, and do not start a card whose dependency is not `done`.

## Delivering one card

1. `scripts/cards.sh pick` (or `claim <ID>`), then `scripts/cards.sh path <ID>` and read the card.
2. Read the previous card's `next:` Journal line when the card has a dependency.
3. Create the worktree and the branch named in the card's frontmatter, off latest `origin/dev`.
4. Follow `delivering-linear-cards` for the work itself. Tick the card's todos in its file as
   they are done.
5. PR open: `scripts/cards.sh set <ID> in-review "PR #<n>"`.
6. PR merged to `dev`: `scripts/cards.sh set <ID> done "PR #<n> · next: <one line the next card
   must know: a seam, a decision, a gotcha>"`. Tick the acceptance criteria in the card file.
7. Clean up the worktree.

Where `delivering-linear-cards` says to set a Linear state, call `scripts/cards.sh set`
instead: `In Progress` is `claimed`, `In Review` is `in-review`, `Done` is `done`.

## Chaining

A `/pick` session does not stop after one card. After each release it goes back to the pick
step and claims the next eligible card. The chain stops by itself, with a report, when:

- nothing is eligible (the Queue is drained, or what remains waits on another session's card);
- a card is `blocked`;
- CI is red twice in a row on the same card;
- a card needs something only the operator can provide (a credential, a decision, a deploy).

Continuity between cards is written down, never remembered: the `next:` line of the Journal
carries what the following card must know.

## Blocking and releasing

- Hit a wall (the approach fights the architecture, a prerequisite is missing): write what was
  learned in the card's Approach, then `scripts/cards.sh set <ID> blocked "<one-line reason>"`.
  Leave `dev` untouched and report a proposed rescope.
- Giving a card back untouched: `scripts/cards.sh set <ID> todo "released: <why>"`.
- A session that must end before the merge leaves a Journal line saying where the card stands.
  A claim with no Journal trail is a protocol violation.
- A card held by a session that is gone: `scripts/cards.sh steal <ID> --by "<who>"`. Only when
  the operator confirms the previous session is dead.

## Creating a card

```
scripts/cards.sh add <ID> <slug> "<title>" --epic <E> --deps "<A,B>" --body-file <file>
```

- The id continues the epic's sequence (`G4` after `G3`); ids are never reused.
- A dependency must already be in the Queue: add the cards of a chain in order. A card cannot
  depend on itself.
- Write the seven sections to a file first and pass it with `--body-file`; without it the card
  gets an empty template to fill in.
- The new row goes to the bottom of the Queue. To place it higher, ask the operator.
- An epic is recorded in the epic map as before; only its cards get Queue rows.
- Edit a card's body only while it is `todo`. A started card is being worked on: open a
  follow-up card instead.

## Reading the queue

```
scripts/cards.sh list    # id, status, dependencies, title, in queue order
scripts/cards.sh next    # the id /pick would take now, or what blocks
```
