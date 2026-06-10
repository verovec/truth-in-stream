# Truth in Stream Roadmap - State File

```
LINEAR_TEAM: Veroveit
LINEAR_PROJECT: Truth in Stream
LINEAR_TICKETS: VER-6..VER-23
AGENT_INDUSTRY_VERSION: 5.0.0
STATUS: Active
CREATED: 2026-06-09
LAST_UPDATED: 2026-06-10
```

Rules live in the `roadmap-linear` skill. This file is derived state; never hand-edit the
Ready queue.

## Card List (live as of 2026-06-10)

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-13 | Local dev stack and layered backend skeleton | Done | High | |
| VER-14 | AWS runtime: Terraform ECS/RDS/ALB/ECR/OIDC | Done | High | |
| VER-6 | Curated verification database: schema + ingestion | Done | High | VER-13 |
| VER-7 | Transcription service: audio to segments | Done | High | VER-13 |
| VER-9 | Embedding and match service | Done | High | VER-6 |
| VER-10 | Processing orchestration | Done | High | VER-7, VER-9 |
| VER-8 | Video player UI | Done | Medium | VER-13 |
| VER-11 | Synced fact-check panel | Done | Medium | VER-8, VER-10 |
| VER-16 | Fix CI startup failure | Done | High | |
| VER-15 | Re-enable backend PR CI + deploy trigger | Done | Medium | VER-16 |
| VER-17 | golangci-lint config enforcing lint standard | In Review | Medium | |
| VER-12 | End-to-end demo and polish | In Review | Medium | VER-6, VER-7, VER-8, VER-9, VER-10, VER-11 |
| VER-18 | Operator authentication and login (single user) | In Progress | High | |
| VER-19 | Wikipedia corpus: schema, dump parsing, and chunk storage | In Progress | High | |
| VER-20 | Wikipedia bulk embedding pipeline with staged HNSW swap | Todo | High | VER-19 |
| VER-21 | Periodic Wikipedia delta sync | Todo | Medium | VER-20 |
| VER-22 | Wikipedia evidence in fact-check results | Todo | Medium | VER-20 |
| VER-23 | Terraform: scheduled wikisync task and RDS sizing readiness | In Progress | Medium | |

## Dependency Graph

```
VER-13 -> VER-6 -> VER-9 -> VER-10 -> VER-11
VER-13 -> VER-7 -> VER-10
VER-13 -> VER-8 -> VER-11
VER-16 -> VER-15
VER-{6,7,8,9,10,11} -> VER-12
VER-19 -> VER-20 -> VER-21
VER-20 -> VER-22
```

## Ready Queue (computed)

A card is READY when state is `Todo` AND every depends_on card is `Done`.

(empty - no card is both `Todo` and unblocked)

Blocked or otherwise not claimable by `/pick`:

- VER-19 (High) - `In Progress`, owned by another session
- VER-23 (Medium) - `In Progress`, owned by another session (CLAIM winner e20df37c)
- VER-20 (High) - blocked by VER-19
- VER-21, VER-22 (Medium) - blocked by VER-20
- VER-17 (Medium) - `In Review`, owned by another session
- VER-12 (Medium) - `In Review`, owned by another session
- VER-18 (High) - `In Progress`, owned by another session

END_OF_DOCUMENT
