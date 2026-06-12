# ROADMAP - TRUTH-IN-STREAM

Synced from Linear project `Truth in Stream` (team Veroveit). Derived state; do not hand-edit
the Ready queue. Rules live in the `roadmap-linear` skill.

Counts: 46 Done, 11 Todo, 1 In Progress (VER-56), 1 In Review (VER-53), 1 Duplicate (VER-25).
Ready queue size: 3.

## Card list

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-6 | Curated verification database: schema and ingestion (pgvector) | Done | High | |
| VER-7 | Transcription service: audio to timestamped segments | Done | High | |
| VER-8 | Video player UI with standard playback features | Done | Medium | |
| VER-9 | Embedding and match service: segments to ranked claims | Done | High | |
| VER-10 | Processing orchestration: run pipeline on load and serve results by timestamp | Done | High | |
| VER-11 | Synced fact-check panel: surface matches for the active segment | Done | Medium | |
| VER-12 | End-to-end demo and polish on one sample video | Done | Medium | |
| VER-13 | Local dev stack and layered backend skeleton | Done | High | |
| VER-14 | AWS runtime: Terraform for ECS Fargate, RDS pgvector, ALB, ECR, OIDC deploy | Done | High | |
| VER-15 | Re-enable backend PR CI and restore the deploy trigger | Done | Medium | |
| VER-16 | Fix CI startup failure: invalid workflow YAML and frontend lockfile drift | Done | High | |
| VER-17 | Add golangci-lint config enforcing the documented backend lint standard | Done | Medium | |
| VER-18 | Operator authentication and login (single user) | Done | High | |
| VER-19 | Wikipedia corpus: schema, dump parsing, and chunk storage | Done | High | |
| VER-20 | Wikipedia bulk embedding pipeline with staged HNSW swap | Done | High | |
| VER-21 | Periodic Wikipedia delta sync | Done | Medium | |
| VER-22 | Wikipedia evidence in fact-check results | Done | Medium | |
| VER-23 | Terraform: scheduled wikisync task and RDS sizing readiness | Done | Medium | |
| VER-24 | Check-worthiness precheck gate for transcript segments | Done | High | |
| VER-25 | Real-time continuous fact-check: live subtitles and verdicts | Duplicate | High | |
| VER-26 | Upload and live-analyse video from the frontend | Done | High | |
| VER-27 | Object storage foundation: MinIO local + S3 on AWS | Done | High | |
| VER-28 | Video metadata and presigned upload API (backend) | Done | High | |
| VER-29 | Upload UI and video library/picker (frontend) | Done | High | |
| VER-30 | Live streaming analysis transport: Scribe v2 Realtime over WebSocket (backend) | Done | High | |
| VER-31 | Live playback sync: stream audio and render incremental fact-checks (frontend) | Done | High | |
| VER-32 | Local dev data environment: one-command reset, seeds, and offline fixtures | Done | High | |
| VER-33 | Public landing page and login-modal entry into the analyser | Done | High | |
| VER-34 | Ingest a YouTube video by link: download, store in object storage, list as playlist item | Done | High | |
| VER-35 | Daily Slack digest and /report command | Done | Low | |
| VER-36 | Library video tiles: render a real frame thumbnail instead of the gradient monogram | Done | Low | |
| VER-37 | Paste a YouTube URL to add a video: frontend ingest affordance and pending-to-ready polling | Done | Medium | |
| VER-38 | Live panel: stacked subtitles-over-fact-checks layout with independent scroll regions | Done | Medium | |
| VER-39 | Live analysis summary: top-of-page running findings strip | Done | Medium | |
| VER-40 | Unify dev seeding: one command for a complete environment | Done | Medium | |
| VER-41 | Speaker-aware live analysis: diarized segmentation, group up to 3 sentences per turn | Done | High | |
| VER-42 | Rework make wiki-populate: auto-load .env keys and skip re-download | Done | Medium | |
| VER-43 | Make AssemblyAI the only transcriber: stream imported videos live, remove ElevenLabs Scribe | Done | Medium | |
| VER-44 | Dev-only debug bar: real-time WebSocket search over embedded wiki corpus | Done | Medium | |
| VER-45 | Fix live speaker labels: split mixed-speaker turns at word boundaries | Done | High | |
| VER-46 | Database backup & restore: full pg_dump with S3 upload to skip re-embedding | Done | Medium | |
| VER-47 | Wiki-grounded coverage: let the embedded corpus decide checkability | Done | High | |
| VER-48 | Live scoring backpressure: stop dropping statements as not_checked under load | Done | High | |
| VER-49 | Structured wiki ingestion: per-chunk metadata for classification | Done | Medium | |
| VER-50 | Confidence scoring: cluster corpus evidence into a corroboration percentage | Done | Medium | |
| VER-51 | RabbitMQ broker and embedding-queue connection (infra + shared client) | Done | High | |
| VER-52 | Embedding worker service: drain the priority queue and embed chunks | Done | High | |
| VER-53 | Adapt ingestion to enqueue embedding jobs and scale to a larger corpus | In Review | Medium | VER-49, VER-52 |
| VER-54 | Topic clustering and importance scoring to drive embedding priority | Todo | Low | VER-53 |
| VER-55 | Full local corpus reingest after the ingestion rework: rebuild chunks and embeddings | Todo | High | VER-49, VER-52, VER-53, VER-54 |
| VER-56 | Scheduled database backups: cron the pg_dump-to-S3 job in the cloud | In Progress | Medium | |
| VER-57 | AWS bootstrap: S3-native-locked tfstate and first-apply readiness on the dev account | Todo | High | |
| VER-58 | Secrets management tooling with AWSCURRENT version pinning per environment | Todo | High | |
| VER-59 | Least-privilege CI/CD IAM roles with terraform-driven policy sync and chicken-egg apply guard | Todo | High | VER-57 |
| VER-60 | Enable the Amazon MQ broker in dev and add per-queue message versioning | Todo | High | VER-59 |
| VER-61 | Deployable producer job on ECS that populates the embedding queue from the corpus | Todo | High | VER-60, VER-53 |
| VER-62 | SSM bastion and port-forward tooling to consume the cloud queue into a local database | Todo | High | VER-61 |
| VER-63 | RabbitMQ metrics lambda and CloudWatch dashboard for the ingestion pipeline | Todo | Medium | VER-62 |
| VER-64 | Worker-lifecycle lambda: queue-depth autoscaling and rollout for ECS embedding workers | Todo | Medium | VER-63 |
| VER-65 | GitHub Action to build and deploy the producer and worker images with queue version roll | Todo | Medium | VER-64 |

## Dependency graph

```
VER-49 -> VER-53
VER-52 -> VER-53
VER-53 -> VER-54
VER-53 -> VER-55
VER-54 -> VER-55
VER-49 -> VER-55
VER-52 -> VER-55
VER-53 -> VER-61
VER-57 -> VER-59
VER-59 -> VER-60
VER-60 -> VER-61
VER-61 -> VER-62
VER-62 -> VER-63
VER-63 -> VER-64
VER-64 -> VER-65
```

## Ready queue

A card is READY when its state is `Todo` AND every `depends_on` card is cleared (Done, or
In Review for a single stacked dependency). Ordered by priority, then unblock-count, then number.

1. VER-57 - AWS bootstrap (High, no deps -> branch off `main`). Unblocks the whole infra chain (VER-59..65).
2. VER-58 - Secrets management tooling (High, no deps -> branch off `main`). Independent operator tooling.
3. VER-54 - Topic clustering and importance scoring (Low, single dep VER-53 In Review -> stack on VER-53 branch).

Not ready:
- VER-55 - blocked by VER-54 (Todo) and VER-53 (In Review); more than one uncleared dependency.
- VER-56 - already `In Progress` (claimed by another session); not a `Todo` candidate.
- VER-59 - blocked by VER-57 (Todo).
- VER-60 - blocked by VER-59 (Todo).
- VER-61 - blocked by VER-60 (Todo) and VER-53 (In Review); VER-60 uncleared.
- VER-62 - blocked by VER-61 (Todo).
- VER-63 - blocked by VER-62 (Todo).
- VER-64 - blocked by VER-63 (Todo).
- VER-65 - blocked by VER-64 (Todo).
