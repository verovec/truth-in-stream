# ROADMAP - TRUTH-IN-STREAM

Generated from live Linear state. Rules live in the `roadmap-linear` skill.

## Card list (open cards; VER-6..VER-73, VER-75..VER-78, VER-77 Done, VER-25 Duplicate)

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-74 | EPIC: Fact-checkable crawl ingestion (API crawl + LLM gate + AWS mirror) | Todo (EPIC, do-not-pick) | High |  |
| VER-75 | Consolidate Anthropic adapters into internal/llm | Done | High |  |
| VER-76 | Category-crawl ingestion pipeline (API crawl, no dump download) | Done | High |  |
| VER-77 | Surface and document confidence-by-closeness | Done | Medium |  |
| VER-78 | Fact-checkability gate in the crawl producer | Done | High | VER-75, VER-76 |
| VER-79 | Auto-prime the broker on docker-compose up under the wiki profile | In Progress | Medium | VER-78 |
| VER-80 | Cloud/AWS wiring for the crawl pipeline | Todo | Medium | VER-79 |

## Dependency graph

VER-75 -> VER-78
VER-76 -> VER-78
VER-78 -> VER-79
VER-79 -> VER-80

## Ready queue

Cards in `Todo` that are READY (every `depends_on` is `Done`, or exactly one is `In Review`
and the rest `Done`), ordered by priority, then unblock-count, then number:

_(empty - VER-79 claimed and now `In Progress`)_

VER-78 merged to `Done`, unblocking VER-79, which was then claimed off this queue and is being
delivered (branched off `main`):

- VER-80 is blocked by VER-79 (`In Progress`); it becomes READY once VER-79 reaches `In Review`.
- VER-74 is the tracking epic (do-not-pick).
