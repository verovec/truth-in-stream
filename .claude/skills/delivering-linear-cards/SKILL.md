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
2. **Branch off `main`.** Never implement on `main`. The branch name MUST be `<TEAM>-<NUMBER>-<slug>`: the card's team identifier and number exactly as Linear shows them (uppercase, e.g. `VER-6`), a single dash, then a slug of 3 to 5 lowercase words joined by underscores. NEVER add a username/author prefix, NEVER use dashes inside the slug, NEVER exceed 5 words. Distill the card title to its essence; do not transcribe it verbatim. Example: card `VER-6 "Curated verification database schema and ingestion (pgvector)"` -> `VER-6-pgvector_database_schema`.
3. **TDD with regression safety.** Write tests first (REQUIRED: superpowers:test-driven-development). Tests must prove the new behavior AND guard existing behavior, so a merge cannot silently break something else. The WHOLE suite must pass, not just the new test.
4. **Run `/code-review`, then apply it.** Before pushing, run `/code-review` and apply the findings. Re-run tests afterward.
5. **Verify green, never push broken code.** Build plus the full test suite pass locally (REQUIRED: superpowers:verification-before-completion). A failing build or a red or skipped test means do not push.
6. **Check the card's boxes** you completed (edit the description, `- [ ]` to `- [X]`).
7. **Open the PR** with a summary and a test plan that references the card. The feature-branch push and PR are the delivery hand-off; that part does not need separate approval.
8. **Set the card to In Review** and link the PR in a comment.
9. **Stop.** The human merges. Do not poll PR state.

## Status transitions

| Event | Card status |
|---|---|
| You start work | In Progress |
| PR opened | In Review |
| Human says merged, or asks you to update | Done (check any remaining boxes) |
| Work blocked on an external dependency (e.g. cloud infra) | leave In Progress; note the blocker in a comment |

## Red flags, stop

- About to push without running `/code-review`: run it and apply it first.
- About to push with a failing build or a red or skipped test: fix first. Never push broken code.
- Handed (or writing) a thin card for another agent to execute: enrich it first.
- Asked to "update Linear" and you are drafting a paragraph of options: just make the update.
- Watching or polling whether the PR merged: not your job. Stop.

## Don't

- Don't merge or deploy on your own; those stay the human's call. (Opening the PR is yours.)
- Don't mark a card Done on PR-open; Done is after merge.
- Don't ship only a happy-path test; include regression coverage.
