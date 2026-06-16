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
| **Producer** | `cmd/wikisync` + `internal/wiki` | Download dump, chunk lead sections, upsert **straight into the live table** (embedding NULL), publish one embed job per chunk. The opt-in `-atomic` mode builds a staging table and swaps it in instead. |
| **Broker** | RabbitMQ (`internal/queue`) | One durable, version-suffixed, priority work queue. |
| **Consumer fleet** | `cmd/embedworker` + `internal/embedjob` + `internal/embed` | Competing consumers; **buffer a batch of chunks, embed them in one Voyage call**, and write the vectors back in place - into the live table by default, or staging for an atomic build. |
| **Store** | `internal/store/postgres` + pgvector | `wiki_chunks` live table, `wiki_chunks_staging` build table (atomic mode only), `wiki_sync_state` checkpoint. |
| **Clusterer** | `cmd/wikicluster` + `internal/cluster` | Spherical k-means over the embedded vectors; writes `cluster_id` + `importance` (priority hints for the next ingest). |
| **Verifier** | `cmd/wikiverify` + `internal/store/postgres/wiki_verify.go` | Gates consistency over the embedded rows and **reports embedded coverage as progress** (no longer requires 100% embedded); exits non-zero on a real defect. |
| **Embedding model** | Voyage `voyage-4-large` | 1024-dim, `halfvec(1024)`, HNSW cosine. `input_type=document` on ingest, `input_type=query` on search. Up to 1000 inputs per request; the fleet batches well under that. |

### Topology

Default (bulk-into-live): chunks land in the live table immediately and each becomes searchable
the moment the fleet writes its vector, so the corpus grows monotonically and is queryable
mid-ingest - no swap.

```mermaid
flowchart LR
    DUMP["dumps.wikimedia.org<br/>{corpus}-latest-pages-articles-<br/>multistream.xml.bz2"]
    subgraph PROD["cmd/wikisync (producer)"]
      D["download + verify<br/>(Last-Modified)"]
      C["extract lead, chunk<br/>(256-512 tok)"]
      U["upsert to wiki_chunks (live)<br/>(embedding NULL; unchanged<br/>chunks keep their vector)"]
      P["publish 1 job/chunk<br/>(priority by importance)"]
    end
    Q[["RabbitMQ<br/>embedding.jobs.v&lt;n&gt;<br/>(priority queue)"]]
    subgraph FLEET["cmd/embedworker x N (competing consumers)"]
      W1["worker 1<br/>(batched embed)"]
      W2["worker 2"]
      WN["worker N"]
    end
    V["Voyage voyage-4-large<br/>input_type=document<br/>(batch per call)"]
    LIVE[("wiki_chunks (live)<br/>halfvec(1024) + HNSW<br/>NULL rows invisible to search")]

    DUMP --> D --> C --> U --> P --> Q
    Q --> W1 & W2 & WN
    W1 & W2 & WN --> V
    V -- "UPDATE ... FROM unnest (in place)" --> LIVE
    LIVE -. "search filters embedding IS NOT NULL" .-> LIVE
```

The opt-in `-atomic` mode keeps the wholesale-cutover shape: ingest builds `wiki_chunks_staging`,
carries forward unchanged vectors, the fleet drains it, then a single transaction builds the HNSW
index and renames staging over live - readers see the old corpus until the swap commits. Use it
for a breaking re-chunk where serving a mix of old and new chunks is unacceptable.

---

## 2. Modes

`cmd/wikisync -mode=<bulk|delta|reset>` plus the `-dry-run` / `-publish-only` / `-atomic` flags.

| Mode / flag | What it does | Touches broker? | Embeds where? |
|-------------|--------------|-----------------|---------------|
| **bulk** (default) | Bulk-into-live: ingest the dump straight into `wiki_chunks` (embedding NULL), record the checkpoint, publish one job per chunk, then **exit**. The fleet fills the vectors in place; the corpus is queryable throughout and grows monotonically. No staging, no swap. | Yes | Fleet to live, in place |
| **bulk `-atomic`** | Build the corpus in `wiki_chunks_staging`, wait for the fleet to drain it, build the HNSW index, then atomically swap it over live. The wholesale-cutover path for a breaking re-chunk. | Yes | Fleet to staging |
| **bulk `-dry-run`** | Report the token/cost estimate of the pending chunks and **stop** (no publish, no embed). With `-atomic` it stages first and estimates the full build; without it, it estimates what is still pending in the live corpus. | No | - |
| **bulk `-atomic -publish-only`** | Atomic ingest + publish jobs, then **exit**. The consumer owns the drain + swap (cloud producer path). | Yes (publish only) | Fleet to staging |
| **delta** | Ask MediaWiki RecentChanges what changed since the checkpoint; refetch + re-embed only those pages **inline**, delete removed pages, advance checkpoint. | No | Inline to live |
| **reset** | Clear the live corpus and its checkpoint so the next bulk run rebuilds from scratch. | No | - |

Mutually exclusive / constraints: `-publish-only` and `-atomic` require `-mode=bulk`; `-publish-only`
and `-dry-run` cannot combine. The default bulk-into-live ingest already publishes and exits, so
`-publish-only` only changes an `-atomic` run (where it skips the drain and swap).

`-max-duration=<dur>` caps wall-clock; the run stops cleanly and the committed prefix resumes on the next run.

---

## 3. End-to-end bulk flow

### Default: bulk-into-live

1. **Plan.** `StagingPlan()` reads the checkpoint; if the live version matches and 0 chunks are
   un-embedded it is a no-op. Otherwise proceed.
2. **Ingest in place.** Stream the dump and `UpsertChunks()` straight into `wiki_chunks` - new and
   changed chunks land with `embedding` NULL, unchanged chunks keep their existing vector (the upsert
   clears the vector only where content changed), and each page's stale chunk tail is trimmed. The live
   HNSW index already exists; NULL rows are simply not in it, so search ignores them.
3. **Checkpoint.** Record the dump version now serving. `liveCurrentAt` also requires 0 un-embedded
   chunks, so a re-run resumes publishing the remainder until the fleet finishes.
4. **Publish + exit.** `RunBulkLivePublish` pages the un-embedded live chunks and publishes one job per
   chunk (stamped for the live corpus), then exits. The fleet embeds them in place; every vector written
   makes its chunk searchable at once, so the corpus grows monotonically and is usable within minutes.

There is no drain-wait and no swap: the corpus is already live. Use `make wiki-verify` to watch embedded
coverage climb.

### Opt-in: `-atomic` (stage-and-swap)

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
- **Prefetch / QoS** is sized to `concurrency x batch_size`, so each replica can fill every batch it
  embeds in parallel - fair dispatch, no single slow worker hoarding more than its in-flight batches.
- The only reason multiple queue names exist is the **versioned-queue convention**
  (`RABBITMQ_QUEUE_VERSIONS`, oldest-first): during a rolling deploy a new consumer can drain `v1` and
  `v2` while producers publish to the newest. These are versions of one logical queue, not per-worker
  queues.

### Per-batch lifecycle (`internal/embedjob`)

Each job is `Job{page_id, chunk_index, content, attempt, staging}`. The worker buffers deliveries up to
`EMBED_WORKER_BATCH_SIZE` (default 128) or `EMBED_WORKER_BATCH_WAIT` (default 200ms), whichever comes
first, then handles the batch:

1. Drop any message that can never succeed individually (unknown queue version, malformed, invalid) -
   acked, so one poison message never sinks the batch.
2. `EmbedDocuments([...])` embeds the **whole batch in one Voyage call**, paying the round-trip once per
   batch instead of once per chunk. A wrong-shape vector for any chunk is dropped individually.
3. Write the batch in **one** `UPDATE wiki_chunks ... FROM unnest($ids, $idxs, $contents, $vectors)` -
   text-form `halfvec` (binary `COPY` corrupts the column). The join matches on **content** as well as
   identity, so a vector is never attached to text it was not computed from (a chunk whose content
   changed under a re-ingest simply does not match and waits for its fresh job). A job with `staging`
   set writes to `wiki_chunks_staging` instead - that is how an `-atomic` build fills staging.
4. **Ack** the whole batch.
5. On a batch-level **embed** failure, fall back to embedding each chunk on its own, so a single bad
   input fails alone while the rest succeed. On a **write** failure, re-enqueue each job at the same
   priority with `attempt+1`. After `EMBED_WORKER_MAX_ATTEMPTS` (default 5) a job is **logged and
   dropped** (no dead-letter). On shutdown mid-batch the deliveries are `Nack`'d with requeue (attempt
   not incremented).
6. If the `UPDATE` matches **no row** (chunk gone, content changed, or staging already swapped away),
   that chunk is simply not updated and its job is dropped as obsolete - not retried.

Below the batch layer, the embed client retries the Voyage call up to 6 times on HTTP 429 / network
timeout. It honours a `Retry-After` header on a 429 (capped at the max backoff) so pacing follows the
provider rather than a fixed guess, and otherwise uses 1s-to-60s exponential backoff + jitter; an
optional per-replica rate limiter (`EMBED_WORKER_RPM`, default 0 = unpaced) is an extra hard cap.

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
    B -->|"formatHalfVec: text form"| C["UPDATE wiki_chunks ... ::halfvec<br/>FROM unnest, match on content"]
    C --> F[("live wiki_chunks<br/>NULL rows invisible")]
    F -->|"SearchWikiChunks<br/>WHERE embedding IS NOT NULL<br/>ORDER BY cosine distance"| G["results"]
    Q2["query text"] -->|"input_type=query"| H["voyage-4-large to 1024-dim vector"] --> G
    I["wikiverify"] -.checks embedded rows.-> F
```

1. **One model, one dimension, one type.** Every vector is `voyage-4-large`, 1024-dim, `halfvec(1024)`.
   `domain.EmbeddingDim = 1024` is validated on both write (`SetChunkEmbeddings`) and query
   (`SearchWiki` uses `pgvector.NewHalfVector` and checks the length). A dim/model mismatch is a bug, not
   a config option.

2. **Symmetric model, asymmetric prompt.** Ingest embeds with `input_type=document`; search embeds the
   query with `input_type=query`. Same model, the input type Voyage expects for each side - this is
   correct usage, not an inconsistency.

3. **Never serve a stale or half-embedded vector.** This holds the same way on every write path because
   `SearchWikiChunks` filters `WHERE embedding IS NOT NULL` and the HNSW index does not contain NULL
   rows - an un-embedded or just-invalidated chunk is simply invisible until its vector lands.
   - **Bulk-into-live & delta:** `UpsertWikiChunk` keeps the existing embedding only if `content` is
     byte-identical; any content change resets `embedding` to **NULL**, so a changed chunk drops out of
     results until re-embedded. The fleet's batched write additionally **matches on content**, so it can
     never attach a vector to text that changed since the job was published.
   - **Atomic:** new content produces new staging rows with NULL embedding; the swap won't proceed until
     every staging row is embedded (below).

4. **In-place growth is monotonic; the atomic swap is wholesale.** In the default bulk-into-live path the
   corpus grows one searchable chunk at a time and is never half-served, because NULL rows are invisible.
   The accepted trade-off on a re-ingest over an existing corpus is that search briefly sees a mix of old
   (still-embedded) and new chunks; the in-place upsert bounds that mix to what actually changed, and a
   first build has no old corpus to mix with. The opt-in `-atomic` path avoids any mix: `swapStaging`
   runs one transaction that **validates** staging is non-empty with **zero** NULL embeddings, then
   renames it into `wiki_chunks` - readers see the old corpus until commit, then the new one, never a
   mixture. Use it for a breaking re-chunk.

5. **Idempotent, at-least-once writes.** A redelivered job rewrites the *same* vector (same content yields
   the same embedding write). A job whose row no longer matches (gone, content changed, or staging already
   swapped) is dropped, not retried. Duplicates from the keyset-paged publisher are therefore harmless.

6. **No binary-COPY corruption.** Vectors are written as pgvector **text** form `[a,b,c]` cast
   `::halfvec` (`formatHalfVec`), never via binary `CopyFrom`/binary `COPY` - pgx binary copy corrupts
   `halfvec` columns (phantom rows). This is structural: every embedding write is a single text-form
   `UPDATE ... FROM unnest(...)` over a `text[]` of vector literals.

7. **Verifier reports coverage, gates consistency** (`cmd/wikiverify`, run by `make wiki-verify`). It no
   longer requires 100% embedded - a bulk-into-live corpus is usable while it fills in - so it **reports
   embedded coverage as progress** and fails only on a real defect over the embedded rows:
   1. chunks present (`count > 0`),
   2. embedded coverage *(reported, never a gate)*,
   3. **no zero-vector embeddings** among embedded rows (`embedding <#> embedding = 0` would mean a zero norm),
   4. **dimension is exactly `halfvec(1024)`** (read from `pg_attribute`),
   5. metadata valid (`kind IN ('lead','body')`),
   6. HNSW index **present**,
   7. HNSW index **valid** (`pg_index.indisvalid`).

8. **Clustering never mutates vectors.** `wikicluster` reads the embedded vectors, runs deterministic
   spherical k-means (seeded PRNG), and writes only `cluster_id` + `importance`. The embeddings are
   read-only input. Idempotent: re-running over an unchanged corpus reproduces the same assignment.

**Honest caveats:**
- The dry-run cost estimate uses a chars/token heuristic and a fixed per-1M-token price - it is an
  estimate, not a billed figure. Without `-atomic` it estimates only what is already pending in live.
- Only **lead** sections are ingested today (`kind='lead'`); `'body'` is reserved. "More data" means more
  *articles* (a bigger `WIKI_CORPUS`), not more depth per article.
- Bulk-into-live does **not** prune fully orphaned pages (gone from the dump entirely); they linger as
  stale evidence until a delta sync deletes them or an `-atomic` rebuild cuts a fresh corpus over. A
  per-page tail trim does remove a shrunk page's stale higher-index chunks.
- A chunk that fails every retry is dropped. In bulk-into-live it just stays un-embedded (coverage never
  reaches 100%); in `-atomic` it leaves staging un-drainable and (correctly) **blocks the swap** - the
  symptom is an `ErrDrainStalled` after 30m, a safety stop that needs operator attention.

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

make wiki-populate                   # paid: ingest into live + publish; the fleet embeds in place
                                     # (resumable, reuses the dump). Searchable as it fills in.
make wiki-cluster                    # optional: cluster + importance (run after embedding)
make wiki-verify                     # reports embedded coverage; green = consistent (not necessarily 100%)
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
| `make wiki-populate` | Bulk-into-live ingest + enqueue; the fleet embeds in place (no swap). Resumable. Add `-atomic` for a stage-and-swap rebuild. |
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
| `EMBED_WORKER_CONCURRENCY` | 4 | In-flight batches per replica. |
| `EMBED_WORKER_BATCH_SIZE` | 128 | Chunks embedded per Voyage call by a worker (<=1000, Voyage limit). The main throughput lever. |
| `EMBED_WORKER_BATCH_WAIT` | 200ms | How long a partial batch waits for more before it is sent, so a quiet queue still drains. |
| `RABBITMQ_PREFETCH` | (= concurrency x batch size) | Unacked jobs the broker holds per replica, sized to fill every concurrent batch. |
| `EMBED_WORKER_MAX_ATTEMPTS` | 5 | Job delivery budget before a chunk is dropped. |
| `EMBED_WORKER_RPM` | 0 (unpaced) | Per-replica Voyage rate cap (extra hard cap; the client also honours 429 `Retry-After`). |
| `WIKI_EMBED_BATCH_SIZE` | 128 | Inline (delta / atomic finalize) embed batch (<=1000, Voyage limit). |
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
| `ErrDrainStalled` after 30m (atomic builds only) | One or more chunks failed every retry, so the staging drain never reaches 0; or the fleet died. | Check worker logs / the RabbitMQ dashboard. Confirm `EMBEDDING_API_KEY`, key quota, and provider health, then re-run with `-atomic` (resumes). The old corpus keeps serving meanwhile. |
| `wiki-verify` shows coverage below 100% | The fleet is still embedding (bulk-into-live fills in over time), or a few chunks were dropped after exhausting retries. | Expected mid-ingest; the corpus is already usable. Leave the fleet running, or re-run `make wiki-populate` to re-publish the remainder. Verify still passes as long as the embedded rows are consistent. |
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

A deployable Fargate task fills the queue: it runs `wikisync -mode=bulk` (bulk-into-live), which
ingests the dump into the live table, publishes one self-contained, versioned job per chunk (each
job carries its content), and exits - the consumer fleet embeds the vectors in place, so the corpus
grows as it drains with no swap. For a wholesale re-chunk cutover, run it with `-atomic -publish-only`
instead, and the consumer owns the drain and swap. The Terraform (`enable_producer`, off by default)
creates a task definition with no schedule; launch a run on demand with the deploy network config the
stack publishes to SSM:

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
live table, or staging under `-atomic`), and publishing is at-least-once with idempotent workers. The
producer needs a database to ingest and read chunks (`enable_rds`, or a tunnelled local database); the
fleet writes the vectors back to that same database.

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
