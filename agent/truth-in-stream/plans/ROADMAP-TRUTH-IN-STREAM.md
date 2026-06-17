# ROADMAP - TRUTH-IN-STREAM

## Card list

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-90 | Speaker-credibility fact-checking (credible/disputed/unverifiable + per-speaker score) | Todo | High |  |
| VER-89 | Live findings strip stuck "in progress" on the verify path (summary not claims-aware) | Done | High | VER-86, VER-88 |
| VER-88 | Golden eval set + accuracy gate; enable FACTCHECK_VERIFY_PATH | Done | High | VER-86 |
| VER-87 | Frontend: per-claim progressive-disclosure verdict rendering | Done | Medium | VER-86 |
| VER-86 | Wire two-pool verify path + per-claim events behind FACTCHECK_VERIFY_PATH | Done | High | VER-83, VER-84 |
| VER-85 | Rework match.go for high-recall retrieval with stable evidence_ids | Done | High |  |
| VER-84 | Evidence verifier package (internal/verify) with citation enforcement | Done | High |  |
| VER-83 | Claim decomposition package (internal/claimdecomp) | Done | High |  |
| VER-82 | Fix category-crawl: -T on sharded producers and follow extracts API continuation | Done | High | VER-81 |
| VER-81 | Harden the category-crawl producer | Done | High |  |
| VER-80 | Cloud/AWS wiring for the crawl pipeline | Done | Medium | VER-79 |
| VER-79 | Auto-prime the broker on docker-compose up under the wiki profile | Done | Medium |  |
| VER-78 | Fact-checkability gate in the crawl producer | Done | High | VER-75 |
| VER-77 | Surface and document confidence-by-closeness | Done | Medium |  |
| VER-76 | Category-crawl ingestion pipeline (API crawl, no dump download) | Done | High |  |
| VER-75 | Consolidate Anthropic adapters into internal/llm | Done | High |  |
| VER-74 | EPIC: Fact-checkable crawl ingestion (API crawl + LLM gate + AWS mirror) | Done | High |  |
| VER-6..VER-73 | (all earlier cards) | Done | — |  |
| VER-25 | Real-time continuous fact-check: live subtitles and verdicts | Duplicate | High |  |

## Dependency graph

```
VER-83 -> VER-86
VER-84 -> VER-86
VER-86 -> VER-88
VER-86 -> VER-87
VER-86 -> VER-89
VER-88 -> VER-89
VER-81 -> VER-82
VER-75 -> VER-78
VER-79 -> VER-80
```

VER-90 names no explicit card dependency; it rides the existing `FACTCHECK_VERIFY_PATH` verify
stack (VER-83/84/85/86/88), all of which are Done.

## Ready queue

Computed: Todo cards whose every depends_on is Done (or one In Review), ordered by priority, unblock-count, number.

1. VER-90 - Speaker-credibility fact-checking (High) - no blocking dependency; verify stack all Done -> READY off main

State counts: 82 Done, 1 Todo (VER-90), 1 Duplicate (VER-25). Ready queue size: 1.
