# ROADMAP - TRUTH-IN-STREAM

## Card list

| ID | Title | State | Priority | depends_on |
|----|-------|-------|----------|------------|
| VER-73 | Incremental, high-throughput wiki embedding: usable mid-ingest | Todo | High | |
| VER-72 | Fresh-clone bootstrap: one verified path from clean checkout to running demo | Done | Low | |
| VER-71 | Local ingestion pipeline: one-command broker+fleet and README quick start | Done | Medium | |
| VER-70 | Live panel UX: newest subtitle on top, fact-check region a minority by default | Done | Urgent | |
| VER-69 | Check-worthiness gate: model-based classifier | Done | Urgent | |
| VER-68 | Live intra-speaker consistency | Done | Urgent | |
| VER-67 | Live coverage marks corpus-grounded statements as "not covered" | Done | High | |
| VER-66 | Fix terraform CI: pass id-token permission | Done | Urgent | |
| VER-65 | GitHub Action to build and deploy producer/worker images | Done | Medium | |
| VER-64 | Worker-lifecycle lambda: queue-depth autoscaling | Done | Medium | |
| VER-63 | RabbitMQ metrics lambda and CloudWatch dashboard | Done | Medium | |
| VER-62 | SSM bastion and port-forward tooling | Done | High | |
| VER-61 | Deployable producer job on ECS | Done | High | |
| VER-60 | Enable Amazon MQ broker in dev, per-queue versioning | Done | High | |
| VER-59 | Least-privilege CI/CD IAM roles | Done | High | |
| VER-58 | Secrets management tooling with AWSCURRENT pinning | Done | High | |
| VER-57 | AWS bootstrap: S3-native-locked tfstate | Done | High | |
| VER-56 | Scheduled database backups | Done | Medium | |
| VER-55 | Full local corpus reingest | Done | High | |
| VER-54 | Topic clustering and importance scoring | Done | Low | |
| VER-53 | Adapt ingestion to enqueue embedding jobs | Done | Medium | |
| VER-52 | Embedding worker service | Done | High | |
| VER-51 | RabbitMQ broker and embedding-queue connection | Done | High | |
| VER-50 | Confidence scoring: corroboration percentage | Done | Medium | |
| VER-49 | Structured wiki ingestion: per-chunk metadata | Done | Medium | |
| VER-48 | Live scoring backpressure | Done | High | |
| VER-47 | Wiki-grounded coverage | Done | High | |
| VER-46 | Database backup & restore | Done | Medium | |
| VER-45 | Fix live speaker labels | Done | High | |
| VER-44 | Dev-only debug bar: WS search over corpus | Done | Medium | |
| VER-43 | Make AssemblyAI the only transcriber | Done | Medium | |
| VER-42 | Rework make wiki-populate | Done | Medium | |
| VER-41 | Speaker-aware live analysis | Done | High | |
| VER-40 | Unify dev seeding | Done | Medium | |
| VER-39 | Live analysis summary strip | Done | Medium | |
| VER-38 | Live panel stacked layout | Done | Medium | |
| VER-37 | Paste YouTube URL to add video | Done | Medium | |
| VER-36 | Library video tiles thumbnail | Done | Low | |
| VER-35 | Daily Slack digest and /report | Done | Low | |
| VER-34 | Ingest a YouTube video by link | Done | High | |
| VER-33 | Public landing page and login modal | Done | High | |
| VER-32 | Local dev data environment | Done | High | |
| VER-31 | Live playback sync (frontend) | Done | High | |
| VER-30 | Live streaming analysis transport (backend) | Done | High | |
| VER-29 | Upload UI and video library/picker | Done | High | |
| VER-28 | Video metadata and presigned upload API | Done | High | |
| VER-27 | Object storage foundation | Done | High | |
| VER-26 | Upload and live-analyse video | Done | High | |
| VER-25 | Real-time continuous fact-check | Duplicate | High | |
| VER-24 | Check-worthiness precheck gate | Done | High | |
| VER-23 | Terraform: scheduled wikisync + RDS sizing | Done | Medium | |
| VER-22 | Wikipedia evidence in fact-check results | Done | Medium | |
| VER-21 | Periodic Wikipedia delta sync | Done | Medium | VER-20 |
| VER-20 | Wikipedia bulk embedding pipeline | Done | High | VER-19 |
| VER-19 | Wikipedia corpus: schema, dump parsing, chunk storage | Done | High | |
| VER-18 | Operator authentication and login | Done | High | |
| VER-17 | golangci-lint config | Done | Medium | |
| VER-16 | Fix CI startup failure | Done | High | |
| VER-15 | Re-enable backend PR CI | Done | Medium | |
| VER-14 | AWS runtime Terraform | Done | High | |
| VER-13 | Local dev stack and backend skeleton | Done | High | |
| VER-12 | End-to-end demo and polish | Done | Medium | VER-6 |
| VER-11 | Synced fact-check panel | Done | Medium | VER-8 |
| VER-10 | Processing orchestration | Done | High | VER-7, VER-9 |
| VER-9 | Embedding and match service | Done | High | VER-7 |
| VER-8 | Video player UI | Done | Medium | |
| VER-7 | Transcription service | Done | High | |
| VER-6 | Curated verification database (pgvector) | Done | High | |

## Dependency graph

VER-19 -> VER-20
VER-20 -> VER-21
VER-6 -> VER-12
VER-8 -> VER-11
VER-7 -> VER-9
VER-7 -> VER-10
VER-9 -> VER-10

## Ready queue

1. VER-73 - Incremental, high-throughput wiki embedding: usable mid-ingest (Todo, High, no deps)
