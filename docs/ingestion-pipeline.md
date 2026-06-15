# Wikipedia Ingestion Pipeline

How the Wikipedia evidence corpus is built, embedded, and kept consistent in Postgres + pgvector.

> Scope: this documents the **wiki evidence** corpus (`wiki_chunks`) only - the bulk/delta
> ingestion path driven by `cmd/wikisync`, `cmd/embedworker`, `cmd/wikicluster`, and `cmd/wikiverify`.
> It is **evidence-only**: it enriches matched claims but does **not** decide coverage/checkability -
> that is the `claims` table. See `.claude/skills/data-map` for the full data dictionary.
>
> This file is a map. Ground truth is the migrations (`stack/backend/migrations/*.up.sql`), the
> sqlc queries (`stack/backend/queries/wiki.sql`), and the commands under `stack/backend/cmd/`.
> If you change the pipeline, update this file in the same change.

---

## 1. Components

| Component | Code | Role |
|-----------|------|------|
| **Producer** | `cmd/wikisync` + `internal/wiki` | Download dump, chunk lead sections, upsert to staging, publish embed jobs, wait for drain, swap live. |
| **Broker** | RabbitMQ (`internal/queue`) | One durable, version-suffixed, priority work queue. |
| **Consumer fleet** | `cmd/embedworker` + `internal/embedjob` + `internal/embed` | Competing consumers; embed each chunk via Voyage and write the vector to staging. |
| **Store** | `internal/store/postgres` + pgvector | `wiki_chunks` live table, `wiki_chunks_staging` build table, `wiki_sync_state` checkpoint. |
| **Clusterer** | `cmd/wikicluster` + `internal/cluster` | Spherical k-means over the embedded vectors; writes `cluster_id` + `importance` (priority hints for the next ingest). |
| **Verifier** | `cmd/wikiverify` + `internal/store/postgres/wiki_verify.go` | Asserts the live corpus is complete and consistent; exits non-zero otherwise. |
| **Embedding model** | Voyage `voyage-4-large` | 1024-dim, `halfvec(1024)`, HNSW cosine. `input_type=document` on ingest, `input_type=query` on search. |

### Topology

```mermaid
flowchart LR
    DUMP["dumps.wikimedia.org<br/>{corpus}-latest-pages-articles-<br/>multistream.xml.bz2"]
    subgraph PROD["cmd/wikisync (producer)"]
      D["download + verify<br/>(Last-Modified)"]
      C["extract lead, chunk<br/>(256-512 tok)"]
      U["upsert to wiki_chunks_staging<br/>(embedding NULL)"]
      CF["carry-forward unchanged<br/>vectors from live"]
      P["publish 1 job/chunk<br/>(priority by importance)"]
    end
    Q[["RabbitMQ<br/>embedding.jobs.v&lt;n&gt;<br/>(priority queue)"]]
    subgraph FLEET["cmd/embedworker x N (competing consumers)"]
      W1["worker 1"]
      W2["worker 2"]
      WN["worker N"]
    end
    V["Voyage voyage-4-large<br/>input_type=document"]
    ST[("wiki_chunks_staging<br/>halfvec(1024)")]
    SWAP{{"drain == 0 ?<br/>build HNSW + atomic swap"}}
    LIVE[("wiki_chunks (live)<br/>+ HNSW index")]

    DUMP --> D --> C --> U --> CF --> P --> Q
    Q --> W1 & W2 & WN
    W1 & W2 & WN --> V --> ST
    ST -. poll COUNT(embedding IS NULL) .-> SWAP
    SWAP -- yes --> LIVE
    LIVE -. carry-forward .-> CF
```

---

## 2. Modes

`cmd/wikisync -mode=<bulk|delta|reset>` plus the `-dry-run` / `-publish-only` flags.

| Mode / flag | What it does | Touches broker? | Embeds where? |
|-------------|--------------|-----------------|---------------|
| **bulk** (default) | Full dump ingest into staging, publish jobs, wait for fleet to drain, build index, atomic swap live. | Yes | Fleet to staging |
| **bulk `-dry-run`** | Ingest into staging, report token/cost estimate, **stop**. No publish, no embed, no swap. | No | - |
| **bulk `-publish-only`** | Ingest + publish jobs, then **exit**. The consumer owns the drain + swap (cloud producer path). | Yes (publish only) | Fleet to staging |
| **delta** | Ask MediaWiki RecentChanges what changed since the checkpoint; refetch + re-embed only those pages **inline**, delete removed pages, advance checkpoint. | No | Inline to live |
| **reset** | Clear the live corpus and its checkpoint so the next bulk run rebuilds from scratch. | No | - |

Mutually exclusive: `-publish-only` requires `-mode=bulk`; `-publish-only` and `-dry-run` cannot combine.

`-max-duration=<dur>` caps wall-clock; the run stops cleanly and the committed prefix resumes on the next run.

---

## 3. End-to-end bulk flow

```mermaid
sequenceDiagram
    participant S as wikisync (producer)
    participant DB as Postgres
    participant Q as RabbitMQ
    participant W as embedworker fleet
    participant Vy as Voyage

    Note over S,DB: Plan
    S->>DB: StagingPlan() read checkpoint + staging stamp
    alt live version matches AND 0 NULL embeddings
        DB-->>S: PlanAlreadyCurrent
        S-->>S: log "already current", exit (no-op)
    else
        DB-->>S: PlanBuild / PlanResumeEmbed
    end

    Note over S,DB: Ingest (build)
    S->>S: download + verify dump (Last-Modified pair)
    S->>DB: ResetStaging() CREATE wiki_chunks_staging (LIKE live), stamp building:version
    loop per article (parallel by stream)
        S->>S: extract lead, chunk (256-512 tokens, title-prefixed)
        S->>DB: UpsertStagingChunks() embedding NULL
    end
    S->>DB: CarryForwardEmbeddings() copy live vectors for byte-identical content
    S->>DB: MarkStagingReady() stamp ready:version

    Note over S,Q: Publish (at-least-once, keyset paged)
    loop pages of unembedded staging chunks
        S->>Q: Publish Job{page_id, chunk_index, content} @ priority
    end

    Note over Q,Vy: Drain (competing consumers)
    par fleet
        W->>Q: consume (prefetch = concurrency)
        W->>Vy: EmbedDocuments([content]) input_type=document
        Vy-->>W: float32[1024]
        W->>DB: UPDATE wiki_chunks_staging SET embedding = $1::halfvec
        W->>Q: ack (retry w/ attempt++ on failure, drop after MAX_ATTEMPTS)
    end

    Note over S,DB: Finalize
    loop poll every WIKI_DRAIN_POLL_INTERVAL
        S->>DB: CountUnembeddedStaging()
    end
    Note right of S: count 0 means proceed. No progress for stall timeout means abort (resumable)
    S->>DB: buildStagingIndex() PK + HNSW (m=16, ef_construction=200)
    S->>DB: swapStaging() TX: validate (non-empty, 0 NULL), rename staging to live, advance checkpoint
    Note over DB: Readers see old corpus until COMMIT, then new corpus atomically
```

### Stage detail

**Download** (`internal/wiki/download.go`). Fetches `{corpus}-latest-pages-articles-multistream.xml.bz2`
and its index from `dumps.wikimedia.org`. The dump and index must share a `Last-Modified` value (the
**dump version**). Files are reused via `If-Modified-Since`/304 and written atomically (temp + rename,
with a `.last-modified` sidecar).

**Chunk** (`internal/wiki/lead.go`, `chunk.go`). Keeps namespace-0 articles, skips redirects/disambigs/empty
leads. `ExtractLead` strips wikitext (refs, templates, links, markup) down to the lead paragraphs. `Chunk`
packs paragraphs into chunks of **256-512 tokens** (about 4 chars/token heuristic), splitting on paragraph,
then sentence, then word, then rune boundaries as needed. Each chunk is stored as `"{title}\n\n{text}"` and
stamped `kind='lead'`.

**Stage** (`internal/store/postgres/wiki_embed.go`). `ResetStaging` creates `wiki_chunks_staging` as a
`CREATE TABLE ... (LIKE wiki_chunks INCLUDING DEFAULTS)` - **unindexed** so inserts are cheap. Chunks are
inserted with `embedding` **NULL**. `CarryForwardEmbeddings` then copies vectors from the live table for
chunks whose content is **byte-identical**, so a rebuild only re-embeds what actually changed.

**Publish** (`internal/wiki/produce.go`). `publishJobs` pages unembedded staging chunks in keyset order
`(page_id, chunk_index)` and publishes one persistent AMQP message per chunk. The cursor advances only
after a full page is confirmed, so an interrupted run re-publishes at most one page - workers are
idempotent, so duplicates are harmless (**at-least-once**). **Priority** comes from the chunk's carried
`importance` (from a prior cluster run) if present, else by kind (lead = max priority). So on a re-ingest,
the most important content embeds first.

**Drain + swap** (`waitDrained` + `FinalizeStaging`). The producer polls `COUNT(*) WHERE embedding IS NULL`
on staging every `WIKI_DRAIN_POLL_INTERVAL` (default 5s). When it reaches 0 it builds the HNSW index on
staging, then runs the swap transaction. If the count stops dropping for `WIKI_DRAIN_STALL_TIMEOUT`
(default 30m), the run aborts as `ErrDrainStalled` - resumable, not a swap of a partial corpus.

---

## 4. The queue (single shared work queue)

```mermaid
flowchart LR
    P["wikisync<br/>publish"] -->|"1 msg/chunk<br/>priority 0..N"| Q[["embedding.jobs.v&lt;n&gt;<br/>durable, x-max-priority"]]
    Q -->|"fair dispatch<br/>prefetch = concurrency"| W1["worker 1"]
    Q --> W2["worker 2"]
    Q --> W3["worker N"]
    W1 & W2 & W3 -->|ack| Q
```

- **One** durable priority queue, base name `embedding.jobs` (`RABBITMQ_QUEUE`), version-suffixed via
  `VersionedName()`, e.g. `embedding.jobs.v1`. Every message carries an `x-queue-version` header.
- **Not** one queue per worker. Every `embedworker` replica is a **competing consumer** on the same
  queue. Throughput scales with replica count.
- **Prefetch / QoS** is set to the worker's concurrency (default 4), so each replica holds at most that
  many unacked jobs - fair dispatch, no single slow worker hoarding a backlog.
- The only reason multiple queue names exist is the **versioned-queue convention**
  (`RABBITMQ_QUEUE_VERSIONS`, oldest-first): during a rolling deploy a new consumer can drain `v1` and
  `v2` while producers publish to the newest. These are versions of one logical queue, not per-worker
  queues.

### Per-message lifecycle (`internal/embedjob`)

1. Receive `Job{page_id, chunk_index, content, attempt}`.
2. `EmbedDocuments([content])` yields one `float32[1024]` (`input_type=document`).
3. `UPDATE wiki_chunks_staging SET embedding = $1::halfvec WHERE (page_id, chunk_index) = ...`.
4. **Ack.**
5. On a transient failure (embed or write): republish at the same priority with `attempt+1`, then ack the
   original. After `EMBED_WORKER_MAX_ATTEMPTS` (default 5) the job is **logged and dropped** (no
   dead-letter). On shutdown mid-work the delivery is `Nack`'d with requeue (attempt not incremented).
6. If the `UPDATE` matches **no row** (staging already swapped away / chunk gone), the job is dropped as
   obsolete - not retried.

Below the job-retry layer, the embed client itself retries the Voyage call up to 6 times on HTTP 429 /
network timeout with 1s-to-60s exponential backoff + jitter, paced by an optional per-replica rate limiter
(`EMBED_WORKER_RPM`, default 0 = unpaced).

---

## 5. Data model

`stack/backend/migrations/0004_wiki_chunks.up.sql`, `0009_*`, `0010_*`.

### `wiki_chunks` (live) / `wiki_chunks_staging` (build, identical shape)

| Column | Type | Notes |
|--------|------|-------|
| `page_id` | `bigint` | PK part. Wikipedia page id. |
| `chunk_index` | `integer` | PK part. Ordinal within the page. |
| `title`, `url` | `text` | Article metadata. |
| `revision_id` | `bigint` | Source revision; delta diffs against it. |
| `corpus` | `text` | `WIKI_CORPUS` - provenance + checkpoint key. |
| `content` | `text` | Chunk text (`"{title}\n\n{lead}"`). |
| `embedding` | `halfvec(1024)` **NULL** | Filled by the fleet; NULL = unembedded. |
| `section` | `text` `''` | Lead = `''`. |
| `kind` | `text` `'lead'` | `'lead'` today; `'body'` reserved. |
| `cluster_id` | `integer` NULL | Written only by `wikicluster`. |
| `importance` | `double precision` NULL | `[0,1]`, written only by `wikicluster`; seeds next ingest's priority. |
| `synced_at` | `timestamptz` | Last ingest/embed time. |

- **PK** `(page_id, chunk_index)`.
- **Index** `wiki_chunks_embedding_hnsw` - HNSW `halfvec_cosine_ops`, `m=16`, `ef_construction=200`.
  HNSW skips NULL rows, so unembedded chunks cost nothing in the index.

### `wiki_sync_state` (checkpoint)

`corpus` (PK), `dump_version`, `last_change_ts`, `synced_at`. The **dump version** is what makes a re-run
short-circuit as "already current"; a code change to chunking/metadata is invisible to it, so forcing a
rebuild needs `-mode=reset` (or `make reingest`).

---

## 6. Vector consistency - the guarantees

This is the part that matters for trusting search results. Each guarantee maps to a concrete mechanism.

```mermaid
flowchart TD
    A["chunk content"] -->|"input_type=document"| B["voyage-4-large to 1024-dim vector"]
    B -->|"formatHalfVec: text form"| C["UPDATE ... ::halfvec (staging)"]
    C --> D{"drain == 0?"}
    D -->|"validate: non-empty AND 0 NULL"| E["atomic swap TX"]
    E --> F[("live wiki_chunks")]
    F -->|"SearchWikiChunks<br/>WHERE embedding IS NOT NULL<br/>ORDER BY cosine distance"| G["results"]
    Q2["query text"] -->|"input_type=query"| H["voyage-4-large to 1024-dim vector"] --> G
    I["wikiverify"] -.asserts.-> F
```

1. **One model, one dimension, one type.** Every vector is `voyage-4-large`, 1024-dim, `halfvec(1024)`.
   `domain.EmbeddingDim = 1024` is validated on both write (`SetChunkEmbeddings`) and query
   (`SearchWiki` uses `pgvector.NewHalfVector` and checks the length). A dim/model mismatch is a bug, not
   a config option.

2. **Symmetric model, asymmetric prompt.** Ingest embeds with `input_type=document`; search embeds the
   query with `input_type=query`. Same model, the input type Voyage expects for each side - this is
   correct usage, not an inconsistency.

3. **Never serve a stale or half-embedded vector.**
   - **Delta path:** `UpsertWikiChunk` keeps the existing embedding only if `content` is byte-identical;
     any content change resets `embedding` to **NULL**. `SearchWikiChunks` filters `WHERE embedding IS
     NOT NULL`, so a changed-but-not-yet-re-embedded chunk simply drops out of results until its fresh
     vector lands. You never get a vector that does not match its text.
   - **Bulk path:** new content produces new staging rows with NULL embedding; the swap won't proceed
     until every staging row is embedded (below).

4. **Atomic corpus swap.** `swapStaging` runs in a single transaction that (a) **validates** staging is
   non-empty and has **zero** NULL embeddings, then (b) renames staging into `wiki_chunks` and advances
   the checkpoint. Readers see the old corpus until commit, then the new corpus - never a mixture, never
   a half-embedded corpus. A persistently failing chunk keeps the NULL count above zero, so the drain
   never completes and the **old corpus keeps serving** rather than a partial one swapping in.

5. **Idempotent, at-least-once writes.** A redelivered job rewrites the *same* vector (same content yields
   the same embedding write). A job whose row no longer exists (corpus already swapped) is dropped, not
   retried. Duplicates from the keyset-paged publisher are therefore harmless.

6. **No binary-COPY corruption.** Vectors are written as pgvector **text** form `[a,b,c]` cast
   `::halfvec` (`formatHalfVec`), never via binary `CopyFrom`/binary `COPY` - pgx binary copy corrupts
   `halfvec` columns (phantom rows). This is structural in the code: every embedding write is a
   single text-form `UPDATE`.

7. **Verifier as the consistency gate** (`cmd/wikiverify`, run by `make wiki-verify`). Seven checks, all
   must pass or it exits non-zero:
   1. chunks present (`count > 0`),
   2. **all chunks embedded** (`count WHERE embedding IS NULL = 0`),
   3. **no zero-vector embeddings** (`embedding <#> embedding = 0` would mean a zero norm),
   4. **dimension is exactly `halfvec(1024)`** (read from `pg_attribute`),
   5. metadata valid (`kind IN ('lead','body')`),
   6. HNSW index **present**,
   7. HNSW index **valid** (`pg_index.indisvalid`).

8. **Clustering never mutates vectors.** `wikicluster` reads the embedded vectors, runs deterministic
   spherical k-means (seeded PRNG), and writes only `cluster_id` + `importance`. The embeddings are
   read-only input. Idempotent: re-running over an unchanged corpus reproduces the same assignment.

**Honest caveats:**
- The dry-run cost estimate uses a chars/token heuristic and a fixed per-1M-token price - it is an
  estimate, not a billed figure.
- Only **lead** sections are ingested today (`kind='lead'`); `'body'` is reserved. "More data" means more
  *articles* (a bigger `WIKI_CORPUS`), not more depth per article.
- A chunk that fails every retry is dropped and leaves staging un-drainable, which (correctly) **blocks
  the swap** - the symptom is an `ErrDrainStalled` after 30m. That is a safety stop, but it needs operator
  attention (see Troubleshooting).

---

## 7. How to use it

All commands are `make` targets (Compose under the hood). They need the dev stack and a real
`EMBEDDING_API_KEY` in `.env`. The fleet is **paid** and lives behind the `wiki` Compose profile, so a
plain `make up` never starts it.

### First-time bulk build

```bash
make up                              # postgres + migrate + offline seed (no fleet)
make fleet-up EMBEDWORKER_REPLICAS=4 # broker + N competing workers (more = faster drain, same $)

# free cost estimate BEFORE paying for embeds
docker compose --profile wiki run --rm wiki-populate \
  go run ./cmd/wikisync -mode=bulk -dry-run

make wiki-populate                   # paid: ingest, embed, swap live (resumable, reuses the dump)
make wiki-cluster                    # optional: cluster + importance (run after embedding)
make wiki-verify                     # green = corpus complete and consistent
make fleet-down                      # stop the paid workers + broker
```

Watch the queue drain at the RabbitMQ dashboard: <http://localhost:15672> (login `app` / `dev`).

### Ingesting *more / other* content

The volume lever is **`WIKI_CORPUS`** - point it at a bigger or different dump, then force a rebuild
(`make reingest` runs reset, populate, cluster, verify):

```bash
# in .env
WIKI_CORPUS=enwiki     # full English (~6.9M articles) instead of simplewiki (default, ~250k)
```

```bash
docker compose --profile wiki run --rm wiki-populate \
  go run ./cmd/wikisync -mode=bulk -dry-run   # check the (much larger) bill first
make reingest                                  # long, paid, unattended; green verify = ready
```

Valid names are Wikimedia dump names of the form `{lang}wiki` (`enwiki`, `frwiki`, `dewiki`, ...); the
config rejects non-dump names like `frwiktionary`.

### Keeping a corpus fresh

```bash
make wiki-update     # delta sync: only articles changed since the checkpoint (inline embed, no swap)
```

### One-shot full rebuild

```bash
make reingest        # reset, populate, cluster, verify
```

### Key make targets

| Target | Purpose |
|--------|---------|
| `make fleet-up [EMBEDWORKER_REPLICAS=N]` | Start broker + N workers. |
| `make fleet-down` | Stop broker + workers (DB and other services untouched). |
| `make wiki-populate` | Bulk ingest + enqueue; fleet drains and swaps live. Resumable. |
| `make wiki-update` | Incremental delta sync via MediaWiki RecentChanges. |
| `make wiki-cluster` | Cluster + importance-score the embedded corpus. |
| `make wiki-verify` | Assert the live corpus is complete/consistent (exits non-zero on failure). |
| `make reingest` | reset, populate, cluster, verify. |

---

## 8. Configuration knobs

From the root `.env` (read by Compose). Defaults shown.

| Env var | Default | What it controls |
|---------|---------|------------------|
| `WIKI_CORPUS` | `simplewiki` | Which Wikimedia dump = how much/what content. The real volume lever. |
| `EMBEDDING_API_KEY` | - | Voyage key (required for any embed). |
| `EMBEDDING_MODEL` | `voyage-4-large` | Embedding model. Pinned to 1024-dim; don't change without a reindex. |
| `EMBEDWORKER_REPLICAS` | 2 (`make fleet-up`) | Number of competing workers. Linear throughput. |
| `EMBED_WORKER_CONCURRENCY` | 4 | In-flight embeds per replica; also the prefetch. |
| `RABBITMQ_PREFETCH` | (= concurrency) | Unacked jobs the broker holds per replica. |
| `EMBED_WORKER_MAX_ATTEMPTS` | 5 | Job delivery budget before a chunk is dropped. |
| `EMBED_WORKER_RPM` | 0 (unpaced) | Per-replica Voyage rate cap. |
| `WIKI_EMBED_BATCH_SIZE` | 128 | Bulk producer embed batch (<=1000, Voyage limit). |
| `WIKI_EMBED_CONCURRENCY` | - | Bulk pipeline concurrency. |
| `WIKI_ENQUEUE_BATCH_SIZE` | - | Staging page size while publishing. |
| `WIKI_DRAIN_POLL_INTERVAL` | 5s | How often the producer polls the drain. |
| `WIKI_DRAIN_STALL_TIMEOUT` | 30m | Abort a stalled drain (resumable). |
| `WIKI_CLUSTER_K` / `_MAX_ITERS` / `_SEED` | - | k-means cluster count / iterations / seed. |
| `RABBITMQ_QUEUE` / `_QUEUE_VERSIONS` | `embedding.jobs` / `v1` | Queue base name and version roll. |

---

## 9. Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `make wiki-populate` does nothing, logs "already current" | Live corpus matches the dump version and is fully embedded. | Expected. To get more, raise `WIKI_CORPUS`; to rebuild same dump, `make reingest` (clears the checkpoint). |
| Drain never finishes, then `ErrDrainStalled` after 30m | One or more chunks failed every retry and got dropped, leaving NULL embeddings; or the fleet died. | Check worker logs / the RabbitMQ dashboard. Confirm `EMBEDDING_API_KEY`, key quota, and provider health, then re-run `make wiki-populate` (resumes). The old corpus keeps serving meanwhile. |
| `wiki-verify` fails "N chunks with a null embedding" | Swap hasn't happened / a delta left chunks pending. | Re-run `make wiki-populate` (bulk) or let `make wiki-update` finish; re-verify. |
| `wiki-verify` fails on HNSW index | Index missing or invalid after a manual change. | `make reingest` rebuilds the index as part of the swap. |
| Workers idle while jobs sit in the queue | Fleet not up, or wrong queue version. | `make fleet-up`; confirm `RABBITMQ_QUEUE_VERSIONS` matches between producer and workers. |
| Delta refuses to run | No baseline, checkpoint older than the retention window, or a bulk build is in progress (staging table exists). | Run a bulk build first / finish the in-flight bulk; then delta. |
| Provider latency / Voyage timeouts | Provider-side, not a bug. | Tune `WIKI_EMBED_*` / `EMBED_WORKER_RPM`; do not lower the defaults blindly. |

---

## 10. Cloud / production pipeline

The local flow above runs the producer and the fleet on one machine. In production the same two
binaries run as separate workloads against an Amazon MQ for RabbitMQ broker, and the deploy is
human-gated (`deploy.yml`, `workflow_dispatch`-only). Same images, different entry points
(`/wikisync` and `/embedworker`) - there is no separate producer or worker image.

### Versioned queue

The queue is named `<RABBITMQ_QUEUE>.v<version>` and `RABBITMQ_QUEUE_VERSIONS` is a comma-separated,
oldest-first list (default `1`). The newest version is active: the producer publishes to it and
stamps it on every message (an AMQP header, so the job payload is unchanged); a worker drops a
message stamped with a version it does not know rather than mis-processing it. To roll, append a new
version (a new active queue); workers still on the old version drain the old queue, and once it is
empty the old version is removed from the list. Delivery stays at-least-once with publisher confirms
and durable, priority-ordered queues.

### Producer task (on demand)

A deployable Fargate task fills the queue: it runs `wikisync -mode=bulk -publish-only`, which
ingests the dump, publishes one self-contained, versioned job per chunk (each job carries its
content, so the worker needs no database), and exits - the consumer fleet owns the drain and the
live swap. The Terraform (`enable_producer`, off by default) creates a task definition with no
schedule; launch a run on demand with the deploy network config the stack publishes to SSM:

```bash
cd stack/terraform/dev
SUBNETS=$(aws ssm get-parameter --name /truth-in-stream/dev/deploy/private-subnet-ids --query Parameter.Value --output text)
SG=$(aws ssm get-parameter --name /truth-in-stream/dev/deploy/tasks-security-group-id --query Parameter.Value --output text)
aws ecs run-task \
  --cluster "$(terraform output -raw ecs_cluster_name)" \
  --task-definition truth-in-stream-dev-producer \
  --launch-type FARGATE \
  --network-configuration "awsvpcConfiguration={subnets=[$SUBNETS],securityGroups=[$SG],assignPublicIp=DISABLED}"
```

The run is resumable: re-running publishes only the chunks still un-embedded (keyset cursor over the
staging table), and publishing is at-least-once with idempotent workers. The producer needs a
database to stage and read chunks (`enable_rds`, or a tunnelled local database), but writes to no
consumer database - the messages are self-contained.

### Deploy the producer and worker (CI)

After the backend/frontend services roll, `scripts/deploy-ingestion.sh` ships the deployed image
(immutable `sha-<short>` tag, never `:latest`) to the two ingestion workloads:

- **Producer:** registers a new producer task-definition revision pinned to that image, so the next
  on-demand `run-task` uses the exact image that was built and Trivy-scanned.
- **Worker fleet:** invokes the worker-lifecycle **deploy** lambda
  (`truth-in-stream-<env>-workerlifecycle-deploy`) with the image and worker service names. The
  lambda creates and promotes a new task set under the service's EXTERNAL deployment controller, so
  in-flight messages drain on the old task set before it is retired. The workflow never updates the
  worker service directly (which would drop in-flight work).

A workload that is not provisioned yet (producer task def or deploy lambda absent) is skipped, not
fatal. The script is unit-tested with a stubbed `aws` CLI (`scripts/deploy-ingestion.test.sh`).

**Rolling the queue version** is an explicit, gated step in the same deploy: run `deploy.yml` with
`queue_versions` set to the new oldest-first list (e.g. `1,2` to make `2` active while `1` drains).
The deploy stamps `RABBITMQ_QUEUE_VERSIONS=<list>` on the producer revision and each worker family
revision. Leave it empty to deploy the image without touching the version; roll back by re-running
with the previous list. The version lives on the task definitions, so the roll never edits Terraform.

### Drain the cloud queue locally (SSM bastion tunnel)

The develop-locally model keeps data on your machine: the producer fills the cloud queue, then you
run the worker locally against a tunnel so it drains into local Postgres. No cloud database is
involved. The broker is private (AMQPS 5671, VPC-only), so the tunnel goes through a hardened
SSM-only bastion (no SSH, no public IP, IMDSv2 required, SSM managed-core role).

```bash
cd stack/terraform/dev
terraform apply -var enable_bastion=true   # deploy is human-gated; run with elevated creds

aws sso login --profile verovec-dev
./scripts/ssm-port-forward.sh dev          # prints localhost:5671; keep it open
```

The broker speaks AMQPS with a certificate for its real hostname, so point that hostname at the
tunnel and keep the broker URL exactly as the secret holds it:

```bash
BROKER_URL=$(aws secretsmanager get-secret-value --secret-id truth-in-stream/dev/rabbitmq/url \
  --query SecretString --output text --profile verovec-dev)
BROKER_HOST=$(printf '%s' "$BROKER_URL" | sed -E 's#.*@([^:/]+).*#\1#')
echo "127.0.0.1 $BROKER_HOST" | sudo tee -a /etc/hosts   # remove when done
```

In a second terminal, run the worker as a host process (not the compose `embedworker`, which is on
the container network and cannot reach the host tunnel) against the tunnel and local database. Make
sure local Postgres is up (`docker compose up -d postgres`) and that `RABBITMQ_QUEUE` /
`RABBITMQ_QUEUE_VERSIONS` match the producer's run:

```bash
cd stack/backend
RABBITMQ_URL="$BROKER_URL" \
DATABASE_URL='postgres://postgres:dev@localhost:5432/truthinstream?sslmode=disable' \
EMBEDDING_API_KEY="$EMBEDDING_API_KEY" \
  go run ./cmd/embedworker
```

Prerequisites: AWS CLI v2 and the Session Manager plugin. The SSM port-forward has no TCP keepalive,
so an idle tunnel can drop - re-run the script to reconnect. When done, drop the `/etc/hosts` line
and tear the bastion down (`terraform apply -var enable_bastion=false`). Unit-tested with a stubbed
`aws` CLI (`scripts/ssm-port-forward.test.sh`).

---

## 11. Cross-references

- Data dictionary: `.claude/skills/data-map/SKILL.md`
- Design specs: `docs/superpowers/specs/2026-06-10-wikipedia-ingestion-design.md`,
  `docs/superpowers/specs/2026-06-11-wiki-ingest-staging-redesign-design.md`
- Schema: `stack/backend/migrations/0004_wiki_chunks.up.sql`, `0009_*`, `0010_*`
- Queries: `stack/backend/queries/wiki.sql`
- Commands: `stack/backend/cmd/{wikisync,embedworker,wikicluster,wikiverify}/`
- Cloud deploy: `scripts/deploy-ingestion.sh`, `scripts/ssm-port-forward.sh`, `.github/workflows/deploy.yml`
- Infra: `stack/terraform/README.md` (`enable_producer`, `enable_bastion`, `enable_rds`, the `rabbitmq` module)
