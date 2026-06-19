---
name: maintaining-documentation
description: Use when an epic's final card has merged to main and the work may have changed setup, architecture, or user-facing behaviour - to decide whether the README and docs/ need updating, and how to write them. Not after individual cards.
---

# Maintaining Documentation

How user-facing docs are kept truthful, and how they read, in this workspace. Run this check
once per epic, never once per card.

## When to run

After an epic's last card reaches `main` (the epic close-out step in `delivering-linear-cards`).
Ask the decision gate first; only edit docs if it passes.

**Decision gate - skip unless at least one is true:**
- Setup or run steps changed (new command, env var, dependency, port).
- Architecture or the request path changed (new service, provider, store, route).
- User-facing behaviour changed (new feature, auth, surface, public URL).
- A documented fact is now wrong.

Pure internal refactors, test-only changes, and infra an operator never sees need no doc change.
If nothing above is true, record "no doc change needed" and stop.

## What to update

- `README.md` first - it is the entry point.
- The `docs/` page that owns the detail; create a new page only when a topic has no home.
- The Documentation table in `README.md` and the On-demand context list in `CLAUDE.md` whenever a
  `docs/` page is added, renamed, or moved.

## How it reads (the philosophy)

- **Operator-first.** Write for someone bringing the stack up and using it, not for the implementer.
- **README stays high-level; detail lives in `docs/`.** The README orients and links out; it never
  grows into a manual.
- **Quick start is N commands from a clean clone.** Keep the count honest and minimal.
- **Truth only - never document an unbuilt feature.** Every command, flag, URL, and behaviour must
  exist on `main`. No aspirational or "coming soon" docs.
- **Tables for indexes, ASCII for flows.** Stack and docs indexes are tables; pipelines are plain
  ASCII diagrams.
- **Concise, no emojis, no filler.** Short direct sentences in the existing README voice.
- **No secrets.** Reference env vars by name; never paste values.

## Common mistakes

- Documenting the planned state of an in-flight epic before it ships.
- Duplicating `docs/` detail into the README instead of linking out.
- Adding a `docs/` page without listing it in the README and `CLAUDE.md` indexes.
- Running this per card and churning the README on every merge.
