# ROADMAP - TRUTH-IN-STREAM

Generated from live Linear state. Rules live in the `roadmap-linear` skill.

## Card list (open cards; VER-6..VER-73, VER-75, VER-77 Done, VER-25 Duplicate)

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-74 | EPIC: Fact-checkable crawl ingestion (API crawl + LLM gate + AWS mirror) | Todo (EPIC, do-not-pick) | High |  |
| VER-75 | Consolidate Anthropic adapters into internal/llm | Done | High |  |
| VER-76 | Category-crawl ingestion pipeline (API crawl, no dump download) | In Progress | High |  |
| VER-77 | Surface and document confidence-by-closeness | Done | Medium |  |
| VER-78 | Fact-checkability gate in the crawl producer | Todo | High | VER-75, VER-76 |
| VER-79 | Auto-prime the broker on docker-compose up under the wiki profile | Todo | Medium | VER-78 |
| VER-80 | Cloud/AWS wiring for the crawl pipeline | Todo | Medium | VER-79 |

## Dependency graph

VER-75 -> VER-78
VER-76 -> VER-78
VER-78 -> VER-79
VER-79 -> VER-80

## Ready queue

Cards in `Todo` that are READY (every `depends_on` is `Done`, or exactly one is `In Review`
and the rest `Done`), ordered by priority, then unblock-count, then number:

_(empty)_

No card is ready. The crawl-ingestion chain is gated on VER-76, which is still `In Progress`:

- VER-78 is blocked by VER-76 (`In Progress`); VER-75 is `Done`. VER-78 becomes READY (stacked
  on the VER-76 branch) only once VER-76 reaches `In Review`.
- VER-79 is blocked by VER-78 (`Todo`); VER-80 is blocked by VER-79 (`Todo`).
- VER-74 is the tracking epic (do-not-pick).

VER-75 and VER-77 merged since the last sync. Re-run `/roadmap` once VER-76 opens its PR
(reaches `In Review`) to surface VER-78 as the next pickable card.
