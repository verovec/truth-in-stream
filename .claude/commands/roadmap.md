---
description: Show the local card queue - what is in flight, what is next, what is blocked
---

Use the `local-cards` skill for the queue rules. The queue is local and always current: there
is nothing to sync, and nothing is read from Linear.

1. Run `scripts/cards.sh list` for every card with its status and dependencies.
2. Run `scripts/cards.sh next` for the card `/pick` would take now, or the list of what blocks.
3. Read the first lines of the Journal in `cards/ROADMAP.md` for the latest changes.
4. Report the counts per status, the next eligible card, the cards in flight and who holds
   them, and each `todo` card with the dependency it waits on. Change nothing.
