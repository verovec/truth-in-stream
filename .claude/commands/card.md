---
description: Create or update a local card following the workspace card rules
---

Use the `local-cards` skill for where a card lives and how it is registered, and the
`roadmap-linear` skill for the card structure, tone and sizing rules. Read both first. Cards are
local Markdown files; do not create or update anything in Linear.

1. Ask what the card is (feature or bug) and gather the outcome and the acceptance criteria.
2. Run `scripts/cards.sh list` and check for file overlap with the other open cards. Cards run
   one session each, in parallel worktrees, so two cards that touch the same files collide at
   merge. If the work overlaps a card that can run at the same time, fold it into that card
   while it is still `todo` instead of creating a second one. Split overlapping work only
   behind a real dependency.
3. Write the seven sections to a file, then register the card with
   `scripts/cards.sh add <ID> <slug> "<title>" --epic <E> --deps "<A,B>" --body-file <file>`.
   The card starts `todo` at the bottom of the Queue; ask before placing it higher.
4. To update a card, edit its file only while it is `todo`. A card in any started state is
   being worked on: open a follow-up card instead.
5. For a new epic, record it in the epic map as `roadmap-linear` describes.
6. Report each card's path and a one-line summary. Do not paste the card back.
