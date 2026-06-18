# Card-based, epic-aware development digest

Date: 2026-06-18
Status: Approved (design)

## Problem

The daily digest (`cmd/digest` + `internal/report`) leads with a raw git-commit
list: a wall of hashes and file counts that says what files moved, not what was
delivered. It carries no notion of an epic completing, and no view of what work
remains.

We want the digest to speak in cards: which cards shipped (with a short
human-readable description of what was implemented) and what remains, and to fire
automatically when an epic finishes so each epic close-out produces a readable
recap.

## Goals

- Replace the rendered Commits section with a card-centric "Shipped" section in
  both modes. Commits remain an internal input (per-card summary, blocker
  heuristic), not a rendered section.
- A "Remaining" section: the project's not-Done cards, grouped by state.
- An epic mode that recaps one epic's children and then the project's remaining
  work, with the epic named in the header.
- The epic digest fires automatically when an epic's last card merges to Done,
  via the delivering-linear-cards flow; a manual `make digest EPIC=VER-93` runs
  it on demand.
- Every shipped card gets a one-line synthesized description (LLM), degrading
  cleanly to the card title when the LLM is unconfigured or fails.

## Non-goals

- No new always-on cron. The epic trigger rides the existing merge-to-Done step
  the delivering agent already performs.
- No change to the daily cron's scheduling or to the Slack webhook plumbing.
- No multi-project support; the digest stays scoped to a single Linear project.

## Modes

One command, two modes, selected by the presence of `-epic`:

- **Daily mode** (`make digest`, the existing cron/manual trigger):
  - Shipped = cards moved to Done within the window (default 24h).
  - Remaining = the project's not-Done cards grouped by state.
  - Open PRs, Blockers, Notes as today.
- **Epic mode** (`make digest EPIC=VER-93`):
  - Header names the epic (`VER-93 - <title>`).
  - Shipped = the epic's Done children (window-independent).
  - Remaining = project-wide not-Done cards grouped by state.
  - Open PRs, Blockers, Notes as today.

Both modes drop the standalone Commits section.

## Architecture

The package keeps its source-interface + Collector + renderer shape. Changes are
additive.

### Payload (internal/report/report.go)

Add to `Payload`:

- `Mode Mode` - `ModeDaily` or `ModeEpic`.
- `Epic *EpicSummary` - nil in daily mode; in epic mode carries `ID`, `Title`.
- `Shipped []CardSummary` - `{ID, Title, Summary}`. Summary is the synthesized
  line, or empty (renderers fall back to Title).
- `Remaining []CardMove` - project not-Done cards; renderers group by State.

`Commits` stays on the struct (the blocker heuristic consumes the active-card set
derived from it) but no renderer emits a Commits section.

### Card summarizer (new: internal/report/summary.go)

```go
type CardInput struct { ID, Title string; Subjects []string }

type CardSummarizer interface {
    // Summarize returns id -> one-line description. A card absent from the
    // result (or an empty value) falls back to its title at render time.
    Summarize(ctx context.Context, cards []CardInput) (map[string]string, error)
}
```

- LLM implementation reuses `internal/llm` (`NewClient`, forced-tool `Classify`),
  mirroring `checkworthy`/`stance`. One batched call for all shipped cards,
  returning an id->summary map via a forced tool schema. Input per card: title +
  its commit subjects (grepped by `VER-id`, window-independent).
- Default model `claude-haiku-4-5-20251001`; deterministic (temperature 0).
- Degradation is first-class: a nil summarizer (no key) means the Collector skips
  summarization and Shipped summaries stay empty; a summarizer error records a
  Note and leaves summaries empty. The digest never fails on the LLM.

### Linear source (internal/report/linear.go)

Add to `LinearSource` / `LinearClient`:

- `EpicChildren(ctx, epicID string) (epicTitle string, children []CardMove, err error)`
  - Resolve the epic by team key + number parsed from `epicID` (e.g. `VER`,`93`),
    read its `title` and `children { nodes { identifier title state { name } } }`.
- `Remaining(ctx) ([]CardMove, error)`
  - Project cards whose state `type` is not `completed`/`canceled`.

The existing `RecentMoves` / `InProgress` are unchanged; daily-mode Shipped is
derived by filtering `RecentMoves` to Done states.

### Git source (internal/report/git.go)

Add to `CommitSource` / `GitCommitSource`:

- `SubjectsForCards(ctx, ids []string) (map[string][]string, error)`
  - `git log --grep` over a bounded scan, grouping commit subjects by the
    `VER-id` they reference. Feeds the summarizer; window-independent so an epic
    recap of older cards still has real input.

### Collector (internal/report/report.go)

`Collect` branches on mode:

- Daily: Shipped from `RecentMoves` filtered to Done; Remaining from `Remaining`.
- Epic: Shipped from `EpicChildren` filtered to Done; `Epic` set from the
  returned title; Remaining from `Remaining`.
- Both: gather `SubjectsForCards` for the shipped IDs, call the summarizer (if
  present), attach summaries. Blockers and Open PRs unchanged.

Mode is selected by a new `CollectorOption` (`WithEpic(id)`); the default is
daily.

### Renderers (slack.go, terminal.go)

Both render, in order: header (epic name when epic mode), Shipped (card ID +
summary, falling back to title), Remaining (grouped by state), Open PRs,
Blockers, Notes. The Commits section is removed. Rendering stays a pure function
of `Payload`.

### Command + Make (cmd/digest/main.go, Makefile)

- `cmd/digest` gains `-epic VER-93`. When set, `buildCollector` adds
  `WithEpic`. The summarizer is wired from `DIGEST_SUMMARY_API_KEY` +
  `DIGEST_SUMMARY_MODEL` (default haiku); unset key -> nil summarizer ->
  degrade to titles.
- `make digest` passes `EPIC` through as `-epic` and keeps `MODE` for
  `terminal` / `dry-run`.

New env (documented in `.env.example`): `DIGEST_SUMMARY_API_KEY`,
`DIGEST_SUMMARY_MODEL`.

## The epic-done trigger (delivering-linear-cards skill)

Code provides the `EPIC=` mode; the automation is a documented step in the
delivering-linear-cards skill. After the delivering agent merges a card and moves
it to Done, if that card has a parent epic, it checks whether all the epic's
siblings are now Done. If so, it runs `make digest EPIC=<parent>` to post the
epic-completion digest. No new always-on process; the check rides the existing
merge-to-Done step.

## Testing

Go, table-driven, `-race` green:

- Collector: daily vs epic Shipped/Remaining grouping; degradation when
  summarizer is nil and when it errors (summaries empty, Note recorded).
- A fake `CardSummarizer` for Collector tests; a fake summary path that asserts
  the id->summary attach.
- `linear.go`: `EpicChildren` and `Remaining` against `httptest` servers
  (success, API-error, empty).
- `git.go`: `SubjectsForCards` over a temp git repo with `VER-id` commit
  subjects.
- Renderers: Slack Block Kit and terminal snapshots for both modes, including the
  title-fallback path.
- Summarizer LLM adapter: forced-tool request shape and map decode, mirroring
  `checkworthy_test`.

E2E: `make digest EPIC=VER-93 MODE=dry-run` renders the epic Block Kit JSON
without posting.

## Risks / open points

- Linear epic = parent issue with children; `EpicChildren` resolves by team key +
  number. If an "epic" is ever modeled as a Linear project/milestone instead, the
  resolver needs a second strategy - out of scope here, noted for later.
- The summarizer adds an Anthropic dependency to a previously API-light command;
  the nil-summarizer default keeps the command fully offline-capable.
