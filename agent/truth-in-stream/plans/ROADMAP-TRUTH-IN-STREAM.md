# Truth in Stream Roadmap - State File

```
LINEAR_TEAM: Veroveit
LINEAR_PROJECT: Truth in Stream
LINEAR_TICKETS: VER-6..VER-34
AGENT_INDUSTRY_VERSION: 5.0.0
STATUS: Active
CREATED: 2026-06-09
LAST_UPDATED: 2026-06-11
```

Rules live in the `roadmap-linear` skill. This file is derived state; never hand-edit the
Ready queue.

## Card List (live as of 2026-06-11)

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
| VER-24 | Check-worthiness precheck gate for transcript segments | Done | High | |
| VER-27 | Object storage foundation: MinIO local + S3 on AWS | Done | High | |
| VER-22 | Wikipedia evidence in fact-check results | Done | Medium | VER-20 |
| VER-21 | Periodic Wikipedia delta sync | In Review | Medium | VER-20 |
| VER-28 | Video metadata and presigned upload API (backend) | Done | High | VER-27 |
| VER-25 | Real-time continuous fact-check: live subtitles and verdicts | Duplicate (canceled) | High | VER-24, VER-30 |
| VER-32 | Local dev data environment: one-command reset, seeds, offline fixtures | In Progress | High | |
| VER-33 | Public landing page and login-modal entry into the analyser | In Review | High | VER-18 |
| VER-34 | Ingest a YouTube video by link (download, store, catalog) | In Progress | High | VER-28 |
| VER-26 | Upload and live-analyse video from the frontend (epic) | Todo | High | VER-28, VER-29, VER-30, VER-31 |
| VER-29 | Upload UI and video library/picker (frontend) | In Review | High | VER-28 |
| VER-30 | Live streaming analysis transport: Scribe v2 Realtime over WebSocket (backend) | In Progress | High | VER-28 |
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
VER-18 -> VER-33
VER-27 -> VER-28 -> VER-29 -> VER-31
VER-28 -> VER-30 -> VER-31
VER-28 -> VER-34
VER-{28,29,30,31} -> VER-26
```

(VER-25 canceled as Duplicate; its VER-24/VER-30 edges are retired.)

## Ready Queue (computed)

A card is READY when state is `Todo` AND every depends_on card is `Done`.

EMPTY - no claimable card. VER-28 is merged to main, which unblocked VER-29, VER-30, and
VER-34; all three were claimed by parallel sessions within minutes (VER-34 00:13:56,
VER-29 00:14:38, VER-30 00:15:35) and are now In Progress. The two remaining Todo cards
are still blocked:

- VER-31 - gated on VER-29 + VER-30 (both In Progress, not Done).
- VER-26 - epic; closes when VER-28..VER-31 are Done.

In flight (claimed by parallel sessions, not claimable): VER-29, VER-30, VER-32,
VER-34 (In Progress); VER-21, VER-33 (In Review, PR #27).

Next unblock: when VER-29 and VER-30 reach Done, VER-31 becomes ready.

END_OF_DOCUMENT
