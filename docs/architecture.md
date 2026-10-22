# Architecture

truth-in-stream is a monorepo: the application code lives under `stack/`, the operator tooling under
`scripts/` and the `Makefile`, the local stack in `docker-compose.yml` (plus
`docker-compose.ingest.yml` for the ingestion fleet). Production runs on AWS from
`stack/terraform`. This page describes the shape of the code and the flow of a claim; the
operational detail lives in [`infrastructure.md`](infrastructure.md) and
[`configuration.md`](configuration.md).

## Layers

```
stack/
  backend/                  Go module github.com/verovec/truth-in-stream/backend (Go 1.26)
    cmd/server              wiring only: config, Postgres, Redis cache, sources, HTTP server
    cmd/seed, cmd/ingest    offline seeding of the demo dataset, media ingestion
    cmd/wikisync, wikicrawl, embedworker, crawlworker
                            the Wikipedia evidence corpus: producers and competing consumers
    cmd/factcheckcrawl, factcheckworker, scrutinscrawl, scrutinsworker, statsingest
                            fact-check archives, parliamentary votes and statistics ingestion
    cmd/scheduler           recurring ingestion (off by default)
    cmd/tvcapture           headless live TV capture worker
    cmd/dbbackup, digest, genhash, vectorbench, wikicluster, wikiverify
                            operator one-shots: backups, Slack digest, password hash, benchmarks
    internal/handler        HTTP only: routing, JSON, status codes, the live WebSocket
    internal/service        use cases, no HTTP types: live session, routing, verification, documents
    internal/store          data access: sqlc-generated queries (store/db), Postgres, analysis cache
    internal/transcribe     AssemblyAI Universal-3 Pro streaming client
    internal/embed          Voyage embeddings (voyage-4-large, 1024 dims)
    internal/llm            LLM providers behind one interface (Anthropic, DeepSeek, Gemini)
    internal/source         evidence sources: statistics, voting records, press, web search
    internal/verify         the evidence-grounded verifier, including the political path
    internal/checkworthy, claimdecomp, claimtype, stance, evidencegate
                            the claim gate, decomposition, typing, stance and evidence filters
    internal/auth, middleware
                            Keycloak token verification and the admin gate
    internal/queue, wiki, crawljob, embedjob, factcheckjob, scrutinsjob
                            the RabbitMQ-backed ingestion jobs
    internal/tvcapture      channel capture, salvage and recordings
    migrations/             golang-migrate SQL files, also the sqlc schema source
    queries/                the SQL that sqlc compiles into internal/store/db
  frontend/                 Next.js 16 dashboard (App Router, React 19, Tailwind v4)
    src/app                 routes: /login, /app, /documents, /tv, /backoffice, i18n under [locale]
    src/components, hooks, lib
    public/pcm-capture-processor.js
                            the AudioWorklet that forwards the player's audio to the main thread
  keycloak/                 the local realm (realm.json) and the production realm template
  terraform/                directory-per-env roots (dev, prod, main-account) and the modules
docs/                       this folder
scripts/                    operator shell tools, each with its own *.test.sh
```

A request goes `handler -> service -> store`. Nothing below `service` knows about HTTP, and the
`/api` subtree and the live WebSocket are gated on the verified Keycloak identity in every
environment. `/healthz` is the only unauthenticated backend route.

## The claim pipeline

Live streams and imported videos share one streaming path; there is no batch transcription route.

1. **Audio** reaches the backend over the live WebSocket: an AudioWorklet taps the media element,
   the main thread resamples it to 16 kHz PCM (`src/lib/live/pcm.ts`) and streams it.
2. **Speech-to-text**: the audio is forwarded to AssemblyAI Universal-3 Pro over its diarizing
   realtime WebSocket (`TRANSCRIPTION_API_KEY`), the single transcriber.
3. **Claim detection**: a check-worthiness gate keeps the factual statements, which are decomposed
   into atomic claims and typed (statistic, voting record, attribution, causal claim).
4. **Retrieval**: the claim is embedded with Voyage `voyage-4-large` (`input_type=query`) and
   searched with pgvector (`halfvec(1024)`, HNSW cosine) over the curated claims and the Wikipedia
   evidence chunks. A near-identical curated claim is a fast path: its verdict is reused without an
   LLM call. Typed claims are routed to the authoritative source for their type (INSEE and Eurostat
   statistics, the Assemblee and Senat voting records, press and web search through Brave); see
   [`fact-check-sources.md`](fact-check-sources.md).
5. **Verification**: the LLM verifier derives a verdict from the retrieved evidence. An optional
   second pass and the political path refine it.
6. **Verdict**: streamed to the fact-check panel and synced to playback. A clean completion of an
   imported or uploaded video is cached (`REDIS_URL`, `ANALYSIS_CACHE_TTL`) so reopening it replays
   transcript and verdicts instantly; a miss falls through to the live pipeline unchanged.

Documents follow the same retrieve-then-verify path once, sentence by sentence, and persist their
verdicts ([`pdf-fact-check.md`](pdf-fact-check.md)). Live TV channels feed their audio through the
same live pipeline from a headless capture worker ([`tv-live.md`](tv-live.md)).

## Surfaces and access

| Route | Access | Role |
|-------|--------|------|
| `/login` | public | Keycloak OIDC login |
| `/app` | authenticated | Video library, live and imported playback with the fact-check panel |
| `/documents` | authenticated | Analysed PDFs with in-place highlights |
| `/tv` | authenticated | Channel grid, live verdicts beside the official embed, recordings |
| `/backoffice` | `admin` realm role | Video upload and YouTube import, document upload, library curation, TV channel switches |

The backend is authoritative: every ingestion mutation carries `middleware.RequireAdmin` and
answers `403` to a token without the `admin` role. The frontend only hides what the user cannot do.
Details in [`backoffice.md`](backoffice.md).

## Data

- **Postgres + pgvector** holds videos, segments and verdicts, the curated
  `claims`, the `wiki_chunks` evidence corpus, documents and their sentence verdicts, TV channels
  and recordings. The embedding model and dimension are pinned (`EMBEDDING_MODEL`, 1024 dims);
  changing them is a full re-ingest ([`embedding-model-migration.md`](embedding-model-migration.md)).
- **Redis or Valkey** is the optional analysis cache, a no-op when unset or unreachable.
- **S3-compatible storage** (MinIO locally, S3 in production) holds media and TV recordings, served
  as presigned URLs.
- **RabbitMQ** carries the ingestion jobs between producers and competing consumers
  ([`ingestion-pipeline.md`](ingestion-pipeline.md)).

The full data dictionary is in `.claude/skills/data-map/SKILL.md`; ground truth is always
`stack/backend/migrations/*.up.sql`.

## Local stack

`docker-compose.yml` defines the services below. Those without a Compose profile start with
`make up` (`docker compose up --build`); the others sit behind the `tools`, `wiki`, `factcheck`,
`scrutins` and `bench` profiles, and the cloud ingestion fleet is in `docker-compose.ingest.yml`.

| Service | Image or build | Port | Profile |
|---------|----------------|------|---------|
| `postgres` | `pgvector/pgvector:pg16` | 5432 | none |
| `migrate` | `migrate/migrate:v4.18.3`, one-shot | | none |
| `seed` | backend image, one-shot, offline | | none |
| `keycloak` | `quay.io/keycloak/keycloak:26.6.3`, imports `stack/keycloak/realm.json` | 8081 | none |
| `backend` | `stack/backend/Dockerfile` | 8080 | none |
| `frontend` | `stack/frontend/Dockerfile` (`node:24-alpine`) | 3000 | none |
| `redis` | `redis:8-alpine` | 6379 | none |
| `minio`, `minio-init` | MinIO and its bucket bootstrap | 9000, 9001 | none |
| `rabbitmq` | `rabbitmq:3.13-management` | 5672, 15672 | none |
| `scheduler`, `tvcapture` | backend image, `cmd/scheduler` and `cmd/tvcapture` | | none |
| `embedworker`, `crawlworker` | backend image | | `wiki` |
| `factcheckworker` / `scrutinsworker` | backend image | | `factcheck` / `scrutins` |
| `wiki-populate`, `wiki-cluster`, `wiki-verify`, `wiki-reset`, `stats-ingest`, `factcheckcrawl`, `scrutinscrawl` | backend image, one-shots | | `tools` |
| `bench-postgres`, `vectorbench` | `pgvector/pgvector:0.8.2-pg17`, backend image | | `bench` |

`make` is the operator entry point: `make help` lists every target (bootstrap, up, down, reset,
seed, backups, tunnels, ingestion, Terraform wrappers).

## Production

Terraform on AWS, region `eu-west-3`, one root per environment. Production is CloudFront and a WAF
in front of an internal ALB, ECS Fargate services for the backend, the frontend and Keycloak, RDS
PostgreSQL 17 with pgvector, an ElastiCache Valkey node for the analysis cache, S3 for media, and
on-demand EC2 hosts for ingestion and TV capture. Logs go to CloudWatch, alarms to SNS and Slack. A
production release is a human-pushed semver tag on `main`. Everything about it is in
[`infrastructure.md`](infrastructure.md) and [`first-setup.md`](first-setup.md).

## Configuration

Every value comes from `.env` (see `.env.example`, grouped and commented); `docker compose`
interpolates it. The essentials:

| Variable | Default | Role |
|----------|---------|------|
| `DATABASE_URL` | compose sets a dev value | Postgres + pgvector connection string |
| `TRANSCRIPTION_API_KEY` | empty | AssemblyAI key, needed only for live analysis |
| `EMBEDDING_API_KEY` / `EMBEDDING_MODEL` | empty / `voyage-4-large` | Voyage key and model, needed for live query embedding and ingestion; seeding is offline |
| `LLM_PROVIDER` and the provider keys | empty | The verifier's LLM (`DEEPSEEK_API_KEY`, `GEMINI_API_KEY`, ...) |
| `REDIS_URL` / `ANALYSIS_CACHE_TTL` | compose wires `redis` / `24h` | Analysis cache for instant replay |
| `KEYCLOAK_ISSUER` / `KEYCLOAK_CLIENT_ID` | the local realm | OIDC issuer and client |
| `PORT` / `CORS_ALLOWED_ORIGIN` | `8080` / unset | Backend listen port, same-origin by default |
| `DOCUMENT_MAX_SIZE_BYTES`, `DOCUMENT_MAX_SENTENCES`, `DOCUMENT_ANALYSIS_TIMEOUT` | 30 MB, `1500`, `30m` | Caps on PDF analysis |
| `SLACK_WEBHOOK_URL` | empty | Ingestion and alarm notices, silent when unset |

The complete table, the auth secrets and the seeded dataset are in
[`configuration.md`](configuration.md).
