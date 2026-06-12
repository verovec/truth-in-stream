# ROADMAP — TRUTH-IN-STREAM

## Card list

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-65 | GitHub Action to build and deploy producer and worker images with queue version roll | Todo | Medium | VER-61, VER-64 |
| VER-64 | Worker-lifecycle lambda: queue-depth autoscaling and rollout for ECS workers | Todo | Medium | VER-60, VER-63 |
| VER-63 | RabbitMQ metrics lambda and CloudWatch dashboard for ingestion pipeline | Todo | Medium | VER-60, VER-62 |
| VER-62 | SSM bastion and port-forward tooling to consume cloud queue into local DB | Todo | High | VER-60, VER-61 |
| VER-61 | Deployable producer job on ECS that populates the embedding queue | Todo | High | VER-53, VER-60 |
| VER-60 | Enable the Amazon MQ broker in dev and add per-queue message versioning | In Progress | High | VER-59 |
| VER-59 | Least-privilege CI/CD IAM roles with terraform-driven policy sync | Done | High | |
| VER-55 | Full local corpus reingest after the ingestion rework | In Progress | High | |
| VER-66 | Fix terraform CI: pass id-token permission from caller to reusable workflow | Done | Urgent | |
| VER-57 | AWS bootstrap: S3-native-locked tfstate and first-apply readiness | Done | High | |
| VER-58 | Secrets management tooling with AWSCURRENT version pinning | Done | High | |
| VER-56 | Scheduled database backups: cron the pg_dump-to-S3 job | Done | Medium | |
| VER-54 | Topic clustering and importance scoring to drive embedding priority | Done | Low | |
| VER-53 | Adapt ingestion to enqueue embedding jobs and scale to a larger corpus | Done | Medium | |
| VER-52 | Embedding worker service: drain the priority queue and embed | Done | High | |
| VER-51 | RabbitMQ broker and embedding-queue connection (infra + shared client) | Done | High | |
| VER-50 | Confidence scoring: cluster corpus evidence into a corroboration score | Done | Medium | |
| VER-49 | Structured wiki ingestion: per-chunk metadata for classification | Done | Medium | |
| VER-48 | Live scoring backpressure: stop dropping statements as not_checked | Done | High | |
| VER-47 | Wiki-grounded coverage: let the embedded corpus decide check-worthiness | Done | High | |
| VER-46 | Database backup & restore: full pg_dump with S3 upload | Done | Medium | |
| VER-45 | Fix live speaker labels: split mixed-speaker turns at word boundaries | Done | High | |
| VER-44 | Dev-only debug bar: real-time WebSocket search over embedded corpus | Done | Medium | |
| VER-43 | Make AssemblyAI the only transcriber: stream imported videos | Done | Medium | |
| VER-42 | Rework make wiki-populate: auto-load .env keys and skip re-download | Done | Medium | |
| VER-41 | Speaker-aware live analysis: diarized segmentation, group updates | Done | High | |
| VER-40 | Unify dev seeding: one command for a complete environment | Done | Medium | |
| VER-39 | Live analysis summary: top-of-page running findings strip | Done | Medium | |
| VER-38 | Live panel: stacked subtitles-over-fact-checks layout | Done | Medium | |
| VER-37 | Paste a YouTube URL to add a video: frontend ingest affordance | Done | Medium | |
| VER-36 | Library video tiles: render a real frame thumbnail | Done | Low | |
| VER-35 | Daily Slack digest and /report command | Done | Low | |
| VER-34 | Ingest a YouTube video by link: download, store in object storage | Done | High | |
| VER-33 | Public landing page and login-modal entry into the analyser | Done | High | |
| VER-32 | Local dev data environment: one-command reset, seeds | Done | High | |
| VER-31 | Live playback sync: stream audio and render incremental fact-checks | Done | High | |
| VER-30 | Live streaming analysis transport: Scribe v2 Realtime over WS | Done | High | |
| VER-29 | Upload UI and video library/picker (frontend) | Done | High | |
| VER-28 | Video metadata and presigned upload API (backend) | Done | High | |
| VER-27 | Object storage foundation: MinIO local + S3 on AWS | Done | High | |
| VER-26 | Upload and live-analyse video from the frontend | Done | High | |
| VER-24 | Check-worthiness precheck gate for transcript segments | Done | High | |
| VER-23 | Terraform: scheduled wikisync task and RDS sizing readiness | Done | Medium | |
| VER-22 | Wikipedia evidence in fact-check results | Done | Medium | |
| VER-21 | Periodic Wikipedia delta sync | Done | Medium | |
| VER-20 | Wikipedia bulk embedding pipeline with staged HNSW swap | Done | High | |
| VER-19 | Wikipedia corpus: schema, dump parsing, and chunk storage | Done | High | |
| VER-18 | Operator authentication and login (single user) | Done | High | |
| VER-17 | Add golangci-lint config enforcing backend lint rules | Done | Medium | |
| VER-16 | Fix CI startup failure: invalid workflow YAML and frontend lint | Done | High | |
| VER-15 | Re-enable backend PR CI and restore the deploy trigger | Done | Medium | |
| VER-14 | AWS runtime: Terraform for ECS Fargate, RDS pgvector, ALB | Done | High | |
| VER-13 | Local dev stack and layered backend skeleton | Done | High | |
| VER-12 | End-to-end demo and polish on one sample video | Done | Medium | |
| VER-11 | Synced fact-check panel: surface matches for the active segment | Done | Medium | |
| VER-10 | Processing orchestration: run pipeline on load and serve results | Done | High | |
| VER-9 | Embedding and match service: segments to ranked claims | Done | High | |
| VER-8 | Video player UI with standard playback features | Done | Medium | |
| VER-7 | Transcription service: audio to timestamped segments | Done | High | |
| VER-6 | Curated verification database: schema and ingestion (pgvector) | Done | High | |
| VER-25 | Real-time continuous fact-check (duplicate) | Duplicate | High | |

## Dependency graph

VER-59 -> VER-60
VER-60 -> VER-61
VER-53 -> VER-61
VER-60 -> VER-62
VER-61 -> VER-62
VER-60 -> VER-63
VER-62 -> VER-63
VER-60 -> VER-64
VER-63 -> VER-64
VER-61 -> VER-65
VER-64 -> VER-65

## Ready queue

(empty)

- VER-60 is the only currently-unblocked link (dependency VER-59 is Done), but it is already
  In Progress — claimed by another session at 10:06. Not available.
- VER-61..65 form a strictly serial chain (VER-61 -> VER-62 -> VER-63 -> VER-64 -> VER-65).
  Each is blocked by the link above it, none of which is Done or In Review yet:
  - VER-61 blocked by VER-60 (In Progress) + VER-53 (Done) — becomes stackable only when VER-60
    reaches In Review.
  - VER-62..65 each wait on the still-Todo card above them.

State counts: 53 Done, 5 Todo, 0 In Review, 2 In Progress, 1 Duplicate. Ready queue size: 0.
