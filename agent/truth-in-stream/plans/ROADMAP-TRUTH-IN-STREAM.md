# ROADMAP - TRUTH-IN-STREAM

## Card list

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-6  | Curated verification database: schema and ingestion | Done | High | |
| VER-7  | Transcription service: audio to timestamped segments | Done | High | |
| VER-8  | Video player UI with standard playback features | Done | Medium | |
| VER-9  | Embedding and match service: segments to ranked claims | Done | High | |
| VER-10 | Processing orchestration: run pipeline on load and serve results | Done | High | |
| VER-11 | Synced fact-check panel: surface matches for the active segment | Done | Medium | |
| VER-12 | End-to-end demo and polish on one sample video | Done | Medium | |
| VER-13 | Local dev stack and layered backend skeleton | Done | High | |
| VER-14 | AWS runtime: Terraform for ECS Fargate, RDS pgvector, ALB, ECR | Done | High | |
| VER-15 | Re-enable backend PR CI and restore the deploy trigger | Done | Medium | |
| VER-16 | Fix CI startup failure: invalid workflow YAML and frontend lint | Done | High | |
| VER-17 | Add golangci-lint config enforcing the documented backend lint | Done | Medium | |
| VER-18 | Operator authentication and login (single user) | Done | High | |
| VER-19 | Wikipedia corpus: schema, dump parsing, and chunk storage | Done | High | |
| VER-20 | Wikipedia bulk embedding pipeline with staged HNSW swap | Done | High | |
| VER-21 | Periodic Wikipedia delta sync | Done | Medium | |
| VER-22 | Wikipedia evidence in fact-check results | Done | Medium | |
| VER-23 | Terraform: scheduled wikisync task and RDS sizing readiness | Done | High | |
| VER-24 | Check-worthiness precheck gate for transcript segments | Done | High | |
| VER-25 | Real-time continuous fact-check: live subtitles and verdicts | Duplicate | High | |
| VER-26 | Upload and live-analyse video from the frontend | Done | High | |
| VER-27 | Object storage foundation: MinIO local + S3 on AWS | Done | High | |
| VER-28 | Video metadata and presigned upload API (backend) | Done | High | |
| VER-29 | Upload UI and video library/picker (frontend) | Done | High | |
| VER-30 | Live streaming analysis transport: Scribe v2 Realtime over WS | Done | High | |
| VER-31 | Live playback sync: stream audio and render incremental fact-checks | Done | High | |
| VER-32 | Local dev data environment: one-command reset, seeds, offline | Done | High | |
| VER-33 | Public landing page and login-modal entry into the analyser | Done | High | |
| VER-34 | Ingest a YouTube video by link: download, store in object storage | Done | High | |
| VER-35 | Daily Slack digest and /report command | Done | Low | |
| VER-36 | Library video tiles: render a real frame thumbnail | Done | Low | |
| VER-37 | Paste a YouTube URL to add a video: frontend ingest affordance | Done | Medium | |
| VER-38 | Live panel: stacked subtitles-over-fact-checks layout | Done | Medium | |
| VER-39 | Live analysis summary: top-of-page running findings strip | Done | Medium | |
| VER-40 | Unify dev seeding: one command for a complete environment | Done | Medium | |
| VER-41 | Speaker-aware live analysis: diarized segmentation, group updates | Done | High | |
| VER-42 | Rework make wiki-populate: auto-load .env keys and skip re-download | Done | Medium | |
| VER-43 | Make AssemblyAI the only transcriber: stream imported videos | Done | Medium | |
| VER-44 | Dev-only debug bar: real-time WebSocket search over embedded corpus | Done | Medium | |
| VER-45 | Fix live speaker labels: split mixed-speaker turns at word boundaries | Done | High | |
| VER-46 | Database backup & restore: full pg_dump with S3 upload | Done | Medium | |
| VER-47 | Wiki-grounded coverage: let the embedded corpus decide checkability | In Review | High | |
| VER-48 | Live scoring backpressure: stop dropping statements as not_checked | In Review | High | |
| VER-49 | Structured wiki ingestion: per-chunk metadata for classification | In Progress | Medium | |
| VER-50 | Confidence scoring: cluster corpus evidence into a corroboration % | Todo | Medium | VER-47, VER-48, VER-49 |
| VER-51 | RabbitMQ broker and embedding-queue connection (infra + client) | In Progress | High | |
| VER-52 | Embedding worker service: drain the priority queue and embed chunks | Todo | High | VER-49, VER-51 |
| VER-53 | Adapt ingestion to enqueue embedding jobs and scale corpus | Todo | Medium | VER-49, VER-52 |
| VER-54 | Topic clustering and importance scoring to drive embedding priority | Todo | Low | VER-53 |

## Dependency graph

VER-47 -> VER-50
VER-48 -> VER-50
VER-49 -> VER-50
VER-49 -> VER-52
VER-49 -> VER-53
VER-51 -> VER-52
VER-52 -> VER-53
VER-53 -> VER-54

## Ready queue

Cards whose state is `Todo` and whose every `depends_on` is `Done`, ordered by
priority, then unblock-count (transitive blocks), then card number.

(empty) - every base card is claimed: VER-51/47/49 In Progress, VER-48 In Review.
The dependent cards VER-50/52/53/54 are blocked: their dependencies are still
In Progress/In Review, not yet Done (merged to main). Nothing is ready to pick.
