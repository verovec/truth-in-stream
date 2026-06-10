# Truth in Stream Roadmap - State File

```
LINEAR_TEAM: Veroveit
LINEAR_PROJECT: Truth in Stream
LINEAR_TICKETS: VER-6..VER-31
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
| VER-17 | golangci-lint config enforcing lint standard | Done | Medium | |
| VER-12 | End-to-end demo and polish | Done | Medium | VER-6, VER-7, VER-8, VER-9, VER-10, VER-11 |
| VER-19 | Wikipedia corpus: schema, dump parsing, and chunk storage | Done | High | |
| VER-23 | Terraform: scheduled wikisync task and RDS sizing readiness | Done | Medium | |
| VER-18 | Operator authentication and login (single user) | Done | High | |
| VER-20 | Wikipedia bulk embedding pipeline with staged HNSW swap | Done | High | VER-19 |
| VER-24 | Check-worthiness precheck gate for transcript segments | Todo | High | |
| VER-25 | Real-time continuous fact-check: live subtitles and verdicts | Todo | High | VER-24 |
| VER-21 | Periodic Wikipedia delta sync | Todo | Medium | VER-20 |
| VER-22 | Wikipedia evidence in fact-check results | Todo | Medium | VER-20 |
| VER-26 | Upload and live-analyse video from the frontend (epic) | Todo | High | VER-27, VER-28, VER-29, VER-30, VER-31 |
| VER-27 | Object storage foundation: MinIO local + S3 on AWS | Todo | High | |
| VER-28 | Video metadata and presigned upload API (backend) | Todo | High | VER-27 |
| VER-29 | Upload UI and video library/picker (frontend) | Todo | High | VER-28 |
| VER-30 | Live streaming analysis transport: Scribe v2 Realtime over WebSocket (backend) | Todo | High | VER-28 |
| VER-31 | Live playback sync: stream audio and render incremental fact-checks (frontend) | Todo | High | VER-29, VER-30 |

## Dependency Graph

```
VER-13 -> VER-6 -> VER-9 -> VER-10 -> VER-11
VER-13 -> VER-7 -> VER-10
VER-13 -> VER-8 -> VER-11
VER-16 -> VER-15
VER-{6,7,8,9,10,11} -> VER-12
VER-19 -> VER-20 -> VER-21
VER-20 -> VER-22
VER-24 -> VER-25
VER-27 -> VER-28 -> VER-29 -> VER-31
VER-28 -> VER-30 -> VER-31
VER-{27,28,29,30,31} -> VER-26
```

## Ready Queue (computed)

A card is READY when state is `Todo` AND every depends_on card is `Done`.

1. VER-27 (High) - Object storage foundation; no dependencies, unblocks the whole video-upload epic (5 downstream cards).
2. VER-24 (High) - Check-worthiness precheck gate; no dependencies, unblocks VER-25.
3. VER-21 (Medium) - Periodic Wikipedia delta sync; VER-20 now Done.
4. VER-22 (Medium) - Wikipedia evidence in fact-check results; VER-20 now Done.

Blocked or otherwise not claimable by `/pick`:

- VER-25 (High) - blocked by VER-24
- VER-28 (High) - blocked by VER-27
- VER-29 (High) - blocked by VER-28
- VER-30 (High) - blocked by VER-28
- VER-31 (High) - blocked by VER-29, VER-30
- VER-26 (High) - epic; tracks VER-27..VER-31, closes when all are `Done`

END_OF_DOCUMENT
