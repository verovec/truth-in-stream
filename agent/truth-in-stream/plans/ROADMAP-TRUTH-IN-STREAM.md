# ROADMAP - TRUTH-IN-STREAM

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
| VER-34 | Ingest a YouTube video by link: download, store in object storage, and list it as a selectable playlist item | Done | High | |
| VER-35 | Daily Slack digest and /report command | Done | Low | |
| VER-36 | Library video tiles: render a real frame thumbnail instead of the gradient monogram | Done | Low | |
| VER-37 | Paste a YouTube URL to add a video: frontend ingest affordance and pending-to-ready polling | Done | Medium | |
| VER-38 | Live panel: stacked subtitles-over-fact-checks layout with independent scroll regions | Done | Medium | |
| VER-39 | Live analysis summary: top-of-page running findings strip | Done | Medium | |
| VER-40 | Unify dev seeding: one command for a complete environment (claims, wiki, demo, sample videos) | Done | Medium | |
| VER-41 | Speaker-aware live analysis: diarized segmentation, group up to 3 sentences per turn | Done | High | |
| VER-42 | Rework make wiki-populate: auto-load .env keys and skip re-download when the dump is already present | Done | Medium | |
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
| VER-53 | Adapt ingestion to enqueue embedding jobs and scale to a larger corpus | Done | Medium | |
| VER-54 | Topic clustering and importance scoring to drive embedding priority | Done | Low | |
| VER-55 | Full local corpus reingest after the ingestion rework: rebuild chunks and embeddings | Done | High | |
| VER-56 | Scheduled database backups: cron the pg_dump-to-S3 job in the cloud | Done | Medium | |
| VER-57 | AWS bootstrap: S3-native-locked tfstate and first-apply readiness on the dev account | Done | High | |
| VER-58 | Secrets management tooling with AWSCURRENT version pinning per environment | Done | High | |
| VER-59 | Least-privilege CI/CD IAM roles with terraform-driven policy sync and chicken-egg apply guard | Done | High | |
| VER-60 | Enable the Amazon MQ broker in dev and add per-queue message versioning | Done | High | |
| VER-61 | Deployable producer job on ECS that populates the embedding queue from the corpus | Done | High | |
| VER-62 | SSM bastion and port-forward tooling to consume the cloud queue into a local database | Done | High | |
| VER-63 | RabbitMQ metrics lambda and CloudWatch dashboard for the ingestion pipeline | Done | Medium | |
| VER-64 | Worker-lifecycle lambda: queue-depth autoscaling and rollout for ECS embedding workers | Done | Medium | |
| VER-65 | GitHub Action to build and deploy the producer and worker images with queue version roll | Done | Medium | |
| VER-66 | Fix terraform CI: pass id-token permission from caller to reusable workflow | Done | Urgent | |
| VER-67 | Live coverage marks corpus-grounded statements as "not covered" (wiki threshold miscalibrated) | Done | High | |
| VER-68 | Live intra-speaker consistency: flag when a speaker contradicts their own earlier statements | Done | Urgent | |
| VER-69 | Check-worthiness gate: model-based classifier so only check-worthy public claims are fact-checked | Done | Urgent | |
| VER-70 | Live panel UX: newest subtitle on top, fact-check region a minority by default, visual polish | Done | Urgent | |
| VER-71 | Local ingestion pipeline: one-command broker+fleet and README quick start | Done | Medium | |
| VER-72 | Fresh-clone bootstrap: one verified path from clean checkout to running demo | Done | Low | |
| VER-73 | Incremental, high-throughput wiki embedding: usable mid-ingest | Done | High | |
| VER-74 | EPIC: Fact-checkable crawl ingestion (API crawl + LLM gate + AWS mirror) | Done | High | |
| VER-75 | Consolidate Anthropic adapters into internal/llm | Done | High | |
| VER-76 | Category-crawl ingestion pipeline (API crawl, no dump download) | Done | High | |
| VER-77 | Surface and document confidence-by-closeness | Done | Medium | |
| VER-78 | Fact-checkability gate in the crawl producer | Done | High | |
| VER-79 | Auto-prime the broker on docker-compose up under the wiki profile | Done | Medium | |
| VER-80 | Cloud/AWS wiring for the crawl pipeline | Done | Medium | |
| VER-81 | Harden the category-crawl producer: retry transient API timeouts and shard CRAWL_CATEGORIES | Done | High | |
| VER-82 | Fix category-crawl: -T on sharded producers and follow extracts API continuation | Done | High | |
| VER-83 | Claim decomposition package (internal/claimdecomp) | Done | High | |
| VER-84 | Evidence verifier package (internal/verify) with citation enforcement | Done | High | |
| VER-85 | Rework match.go for high-recall retrieval with stable evidence_ids | Done | High | |
| VER-86 | Wire two-pool verify path + per-claim events behind FACTCHECK_VERIFY_PATH | Done | High | |
| VER-87 | Frontend: per-claim progressive-disclosure verdict rendering | Done | Medium | |
| VER-88 | Golden eval set + accuracy gate; enable FACTCHECK_VERIFY_PATH | Done | High | |
| VER-89 | Live findings strip stuck "in progress" on the verify path (summary not claims-aware) | Done | High | |
| VER-90 | Speaker-credibility fact-checking (credible/disputed/unverifiable + per-speaker score) | Done | High | |
| VER-91 | Buffer live transcript to complete-sentence boundaries before fact-checking | Done | High | |
| VER-92 | Show "Unverifiable" (not "Unclear") for unverifiable verdicts in the live finding bar | Done | Medium | |
| VER-216 | Durable video analysis storage and read API | Todo | High | |
| VER-217 | ffmpeg audio-extraction adapter and pacing | Todo | High | VER-216 |
| VER-218 | Headless server-side pre-analysis job for imported videos | Todo | High | VER-216, VER-217 |
| VER-219 | Analysed playback plumbing: stored subtitles and pre-analyse control | Todo | High | VER-218 |
| VER-220 | Backoffice: video analysis status and analyse/re-analyse controls | Todo | High | VER-218 |
| VER-221 | Claim timeline strip: verdict-colored playback overview | Todo | Medium | VER-219 |
| VER-222 | Pre-analysis docs and end-to-end verification close-out | Todo | Medium | VER-221, VER-220 |

## Dependency graph

VER-216 -> VER-217
VER-216 -> VER-218
VER-217 -> VER-218
VER-218 -> VER-219
VER-218 -> VER-220
VER-219 -> VER-221
VER-220 -> VER-222
VER-221 -> VER-222

## Ready queue

1. VER-216 - Durable video analysis storage and read API (High, no deps; branches off `dev`)

(The rest of Epic D - VER-217..222 - unblocks in dependency order as each predecessor
reaches Done or In Review. Note: the card list above VER-216 is a stale historical snapshot
that predates VER-93; it is not maintained by this entry.)
