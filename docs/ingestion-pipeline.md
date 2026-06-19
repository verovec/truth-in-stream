# Ingestion Pipeline

How the Wikipedia evidence corpus (`wiki_chunks`) is built, embedded, and kept consistent in
Postgres + pgvector, and how a checked statement's confidence is scored against it.

> **Status.** This document describes the **target** pipeline. The **dump** path
> (`cmd/wikisync` + `cmd/embedworker`) is built and accurate today. The **category-crawl** path
> (`cmd/wikicrawl` + `cmd/crawlworker`), the **fact-checkability gate**, the **compose auto-prime**,
> and the **crawl cloud wiring** are being delivered by epic **VER-74**
> (`docs/superpowers/specs/2026-06-16-checkworthy-crawl-ingestion-design.md`). Sections describing
> unbuilt work are marked **(target - VER-74)**. Ground truth is always the migrations
> (`stack/backend/migrations/*.up.sql`), the sqlc queries (`stack/backend/queries/wiki.sql`), and the
> commands under `stack/backend/cmd/`. If you change the pipeline, update this file in the same change.

> Scope: the **wiki evidence** corpus (`wiki_chunks`). It is **evidence-only**: it enriches matched
> claims but does **not** decide coverage/checkability of a live statement - that is the `claims`
> table. See `.claude/skills/data-map` for the full data dictionary.

---

## 0. Two ingestion paths, one corpus

There are two ways to fill `wiki_chunks`, sharing the broker, the embedding model, the schema, and the
vector-consistency guarantees. They never touch each other's checkpoints or staging.

| | **Dump path** (built) | **Category-crawl path** (target - VER-74) |
|---|---|---|
| Source | Multi-GB Wikimedia dump over HTTP | MediaWiki **Action API**, category-driven, no dump |
| Producer | `cmd/wikisync` | `cmd/wikicrawl` (DB-free) |
| Filter | none (all lead paragraphs) | **fact-checkability LLM gate** in the producer |
| Worker | `cmd/embedworker` | `cmd/crawlworker` |
| Queue | `embedding.jobs.v<n>` | `crawl.chunks.v<n>` |
| Corpus tag | `WIKI_CORPUS` | `CRAWL_CORPUS` (`<project>-crawl`) |
| Lifecycle | bulk-into-live (or `-atomic` swap) | additive upsert into live |
| Use it for | a whole language's worth of leads | a focused, pre-filtered, evidence-only slice |

Both paths produce the same row shape and feed the same search. The crawl path is the one that
satisfies "no dump download," "embed only fact-checkable content," "prime the broker on compose up,"
and "scale by the number of workers I start."

---

## 1. Components

| Component | Code | Role |
|-----------|------|------|
| **Dump producer** | `cmd/wikisync` + `internal/wiki` | Download dump, chunk lead sections, upsert into the live table (embedding NULL), publish one embed job per chunk. Opt-in `-atomic` builds a staging table and swaps it in. |
| **Crawl producer** *(target - VER-74)* | `cmd/wikicrawl` + `internal/wiki/crawl.go`,`crawlproduce.go` | DB-free: traverse Wikipedia categories over the Action API, extract lead + body, chunk, **gate each chunk for fact-checkability**, publish one self-contained job per passing chunk, exit. |
| **Fact-checkability gate** *(target - VER-74)* | `internal/llm` + the gate classifier | Per-chunk LLM judgment "is this verifiable, citable factual evidence?" in the crawl producer, before publish. Fail-open on error. |
| **Broker** | RabbitMQ (`internal/queue`) | Durable, version-suffixed, priority work queues - one per path (`embedding.jobs`, `crawl.chunks`). |
| **Embed fleet (dump)** | `cmd/embedworker` + `internal/embedjob` + `internal/embed` | Competing consumers; buffer a batch, embed in one Voyage call, write vectors back in place. |
| **Crawl fleet** *(target - VER-74)* | `cmd/crawlworker` + `internal/crawljob` | Competing consumers; embed each self-contained job, upsert content + vector into live in one statement. |
| **Store** | `internal/store/postgres` + pgvector | `wiki_chunks` live, `wiki_chunks_staging` (atomic only), `wiki_sync_state` checkpoint. |
| **Clusterer** | `cmd/wikicluster` + `internal/cluster` | Spherical k-means over embedded vectors; writes `cluster_id` + `importance`. |
| **Verifier** | `cmd/wikiverify` | Gates consistency over embedded rows, reports embedded coverage as progress; non-zero on a real defect. |
| **Confidence scorer** | `internal/service/confidence.go` | At search time, aggregates a statement's closeness to matched chunks **and** curated claims into one score. Read-only over the corpus. |
| **Embedding model** | Voyage `voyage-4-large` | 1024-dim, `halfvec(1024)`, HNSW cosine. `input_type=document` on ingest, `input_type=query` on search. <=1000 inputs/request. |

---

## 2. The fact-checkability gate *(target - VER-74)*

The point of the crawl path is to embed **only content that can serve as fact-check evidence**, not all
of Wikipedia's prose. The gate is an LLM classifier that runs **in the crawl producer**, on each chunk,
after chunking and before publishing.

- **Judgment.** "Does this passage contain verifiable, citable factual content suitable as fact-check
  evidence?" - a forced-tool, temperature-0 call on the cheapest fast Claude model (Haiku-class). It is
  distinct from `internal/checkworthy`, which judges a single short **spoken statement** on the live
  path; this judges a 256-512-token wiki passage. Both are thin callers over the shared `internal/llm`
  transport.
- **Placement (producer, by design).** Only passing chunks reach the broker, so the broker volume and
  **all** downstream embedding spend are bounded to evidence. The producer stays DB-free. The trade-off:
  the gate decision is baked at crawl time - re-tuning the prompt/threshold means re-crawling.
- **Fail-open.** On any gate error (transport, malformed reply) the chunk is **published anyway** and the
  error logged, so a flaky model can never silently empty the corpus. The live `checkworthy` cascade
  degrades to a heuristic; the ingest gate has none, so it fails open.
- **Kill-switch.** `CRAWL_CHECKWORTHY=false` publishes every chunk (the pre-gate behavior). The gate is
  paced by `CRAWL_CHECKWORTHY_RPM` and bounded by `CRAWL_MAX_PAGES`.

```mermaid
flowchart LR
    X["chunk (lead/body)"] --> G{"internal/llm gate<br/>citable factual evidence?"}
    G -- pass --> P["publish CrawlJob"]
    G -- fail --> D["drop + count"]
    G -- error --> P
```

---

## 3. Dump-path topology (built)

Default (bulk-into-live): chunks land in the live table immediately and each becomes searchable the
moment the fleet writes its vector, so the corpus grows monotonically and is queryable mid-ingest - no
swap.

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

The opt-in `-atomic` mode keeps the wholesale-cutover shape: ingest builds `wiki_chunks_staging`, carries
forward unchanged vectors, the fleet drains it, then a single transaction builds the HNSW index and
renames staging over live - readers see the old corpus until the swap commits. Use it for a breaking
re-chunk where serving a mix of old and new chunks is unacceptable.

## 3b. Crawl-path topology *(target - VER-74)*

```mermaid
flowchart LR
    CAT["Wikipedia<br/>Action API"]
    subgraph PRODC["cmd/wikicrawl (producer, DB-free, + LLM)"]
      M["categorymembers BFS<br/>depth + page cap"]
      X["prop=extracts<br/>lead + full body"]
      C2["Chunk() lead/body"]
      G2["gate: citable evidence?"]
      P2["publish passing chunks"]
    end
    QC[["RabbitMQ<br/>crawl.chunks.v&lt;n&gt;"]]
    subgraph FLEETC["cmd/crawlworker x N"]
      CW1["worker 1"]
      CWN["worker N"]
    end
    Vc["Voyage voyage-4-large"]
    LIVEC[("wiki_chunks (live) + HNSW")]

    CAT --> M --> X --> C2 --> G2 --> P2 --> QC
    QC --> CW1 & CWN
    CW1 & CWN --> Vc
    CW1 & CWN -->|"UpsertEmbeddedChunk (content + vector)"| LIVEC
```

The producer never touches the database; the worker never touches the dump pipeline's staging table.
They communicate only through self-contained broker messages, so the broker can hold a complete,
pre-filtered corpus indefinitely before any worker is started - the core convenience of this path.

---

## 4. Modes & flags

### Dump path (`cmd/wikisync -mode=<bulk|delta|reset>`)

| Mode / flag | What it does | Touches broker? | Embeds where? |
|-------------|--------------|-----------------|---------------|
| **bulk** (default) | Bulk-into-live: ingest the dump straight into `wiki_chunks` (embedding NULL), checkpoint, publish one job per chunk, exit. The fleet fills vectors in place; the corpus is queryable throughout. | Yes | Fleet to live, in place |
| **bulk `-atomic`** | Build in `wiki_chunks_staging`, wait for the fleet to drain, build HNSW, atomically swap over live. | Yes | Fleet to staging |
| **bulk `-dry-run`** | Report the token/cost estimate of the pending chunks and stop. | No | - |
| **bulk `-atomic -publish-only`** | Atomic ingest + publish, then exit; the consumer owns the drain + swap (cloud producer path). | Yes (publish only) | Fleet to staging |
| **delta** | Ask MediaWiki RecentChanges what changed since the checkpoint; refetch + re-embed only those pages inline, delete removed pages, advance checkpoint. | No | Inline to live |
| **reset** | Clear the live corpus + checkpoint so the next bulk run rebuilds from scratch. | No | - |

Constraints: `-publish-only` and `-atomic` require `-mode=bulk`; `-publish-only` and `-dry-run` cannot
combine. `-max-duration=<dur>` caps wall-clock; the committed prefix resumes on the next run.

### Crawl path (`cmd/wikicrawl`) *(target - VER-74)*

No modes. One run = crawl `CRAWL_CATEGORIES`, gate, publish passing chunks, exit. Always additive into
live. Re-running is harmless (idempotent upserts). Knobs are env (section 11), not flags.
`CRAWL_CHECKWORTHY=false` disables the gate.

---

## 5. End-to-end flows

### Dump default: bulk-into-live (built)

1. **Plan.** `StagingPlan()` reads the checkpoint; if the live version matches and 0 chunks are
   un-embedded it is a no-op.
2. **Ingest in place.** Stream the dump and `UpsertChunks()` straight into `wiki_chunks` - new/changed
   chunks land with `embedding` NULL, unchanged chunks keep their vector, each page's stale chunk tail is
   trimmed. NULL rows are not in the HNSW index, so search ignores them.
3. **Checkpoint.** Record the dump version now serving.
4. **Publish + exit.** `RunBulkLivePublish` pages the un-embedded live chunks and publishes one job per
   chunk, then exits. The fleet embeds in place; the corpus grows monotonically and is usable within
   minutes. Use `make wiki-verify` to watch coverage climb.

### Dump opt-in: `-atomic` (stage-and-swap)

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
    S->>S: download + verify dump
    S->>DB: ResetStaging() + stamp building:version
    loop per article
        S->>DB: UpsertStagingChunks() embedding NULL
    end
    S->>DB: CarryForwardEmbeddings() + MarkStagingReady()
    Note over S,Q: Publish (at-least-once, keyset paged)
    loop pages of unembedded staging chunks
        S->>Q: Publish Job @ priority
    end
    Note over Q,Vy: Drain (competing consumers)
    par fleet
        W->>Q: consume (prefetch = concurrency)
        W->>Vy: EmbedDocuments([content]) input_type=document
        W->>DB: UPDATE wiki_chunks_staging SET embedding
        W->>Q: ack
    end
    Note over S,DB: Finalize
    loop poll every WIKI_DRAIN_POLL_INTERVAL
        S->>DB: CountUnembeddedStaging()
    end
    S->>DB: buildStagingIndex() + swapStaging() TX
    Note over DB: Readers see old corpus until COMMIT, then new corpus atomically
```

### Crawl flow *(target - VER-74)*

1. **Crawl.** `CategoryMembers` issues `action=query&list=categorymembers` over the Action API, BFS over
   subcategories to `CRAWL_MAX_DEPTH`, dedupes page ids, stops at `CRAWL_MAX_PAGES`, follows continuation,
   honors maxlag/Retry-After.
2. **Extract.** Per batch of titles: `Extracts` (lead, `exintro`) + `FullExtracts` (full plain text +
   revision). Body = full text with the lead prefix stripped.
3. **Chunk + index.** `Chunk(lead)` produces `kind=lead`, `Chunk(body)` produces `kind=body`; contiguous
   `chunk_index` (leads `0..L-1`, bodies `L..`), keeping the `(page_id, chunk_index)` PK stable.
4. **Gate.** Each chunk is judged for fact-checkability (section 2); failing chunks are dropped and counted.
5. **Publish.** One self-contained `CrawlJob` per passing chunk, publisher confirms (at-least-once).
   Priority: `lead = max`, `body = max/2`.
6. **Consume.** `crawljob.Worker`: unmarshal -> validate -> `EmbedDocuments([content])` -> check shape
   1x1024 -> `UpsertEmbeddedChunk(chunk, embedding)` (content **and** vector in one statement). A row is
   never visible to search without its matching vector.

---

## 6. The queues (one shared work queue per path)

```mermaid
flowchart LR
    P["producer<br/>publish"] -->|"1 msg/chunk<br/>priority 0..N"| Q[["jobs.v&lt;n&gt;<br/>durable, x-max-priority"]]
    Q -->|"fair dispatch<br/>prefetch = concurrency"| W1["worker 1"]
    Q --> W2["worker 2"]
    Q --> W3["worker N"]
    W1 & W2 & W3 -->|ack| Q
```

- Each path has **one** durable priority queue (`embedding.jobs` for the dump fleet, `crawl.chunks` for
  the crawl fleet), version-suffixed via `VersionedName()` (e.g. `embedding.jobs.v1`). Every message
  carries an `x-queue-version` header.
- Every worker replica is a **competing consumer** on the same queue. Throughput scales with replica
  count - bringing up more workers drains faster for the same spend.
- **Prefetch / QoS** is sized to in-flight batches, so no single slow worker hoards work.
- Multiple queue *names* exist only for the **versioned-queue convention** (`RABBITMQ_QUEUE_VERSIONS`,
  oldest-first): during a rolling deploy a new consumer drains old versions while producers publish to
  the newest. These are versions of one logical queue, not per-worker queues.

### Dump per-batch lifecycle (`internal/embedjob`)

Each job is `Job{page_id, chunk_index, content, attempt, staging}`. The worker buffers deliveries up to
`EMBED_WORKER_BATCH_SIZE` (128) or `EMBED_WORKER_BATCH_WAIT` (200ms), then:

1. Drop any message that can never succeed (unknown version, malformed) - acked, so one poison message
   never sinks the batch.
2. `EmbedDocuments([...])` embeds the **whole batch in one Voyage call**.
3. Write in **one** `UPDATE wiki_chunks ... FROM unnest(...)` - text-form `halfvec`, matching on
   **content** as well as identity so a vector is never attached to text it was not computed from. A
   `staging` job writes to `wiki_chunks_staging`.
4. **Ack** the batch.
5. On batch **embed** failure, fall back to per-chunk embed; on **write** failure, re-enqueue each job at
   `attempt+1`. After `EMBED_WORKER_MAX_ATTEMPTS` (5) a job is logged and dropped. Shutdown mid-batch
   Nacks with requeue (attempt not incremented).
6. An `UPDATE` matching no row (chunk gone, content changed, staging swapped) drops the job as obsolete.

Below the batch layer the embed client retries Voyage up to 6x on 429/timeout, honoring `Retry-After`,
otherwise 1s-60s exponential backoff + jitter; `EMBED_WORKER_RPM` is an optional per-replica hard cap.

### Crawl per-message lifecycle (`internal/crawljob`) *(target - VER-74)*

Each job is a self-contained `CrawlJob` (page id, chunk index, title, url, revision, corpus, content,
section, kind, attempt). The worker mirrors `embedjob` semantics: malformed/invalid/unknown-version
ack-drops; a provider shape other than 1x1024 ack-drops; a transient embed/write failure republishes at
`attempt+1`, dropped after `CRAWL_WORKER_MAX_ATTEMPTS`; shutdown Nacks with requeue. The upsert writes
content + vector in one statement, keeping the existing embedding only when content is byte-identical.

---

## 7. Data model

`stack/backend/migrations/0004_wiki_chunks.up.sql`, `0009_*`, `0010_*`. The crawl path adds **no
migration** - it writes the same `wiki_chunks` shape.

### `wiki_chunks` (live) / `wiki_chunks_staging` (build, identical shape)

| Column | Type | Notes |
|--------|------|-------|
| `page_id` | `bigint` | PK part. Wikipedia page id. |
| `chunk_index` | `integer` | PK part. Ordinal within the page. |
| `title`, `url` | `text` | Article metadata. |
| `revision_id` | `bigint` | Source revision; delta diffs against it. |
| `corpus` | `text` | `WIKI_CORPUS` (dump) or `CRAWL_CORPUS` (crawl) - provenance + checkpoint key. |
| `content` | `text` | Chunk text (`"{title}\n\n{text}"`). |
| `embedding` | `halfvec(1024)` **NULL** | Filled by the fleet; NULL = unembedded (invisible to search). |
| `section` | `text` `''` | Lead = `''`; body headings are reserved (`''` until `action=parse`). |
| `kind` | `text` `'lead'` | `'lead'` (dump) or `'lead'`/`'body'` (crawl). |
| `cluster_id` | `integer` NULL | Written only by `wikicluster`. |
| `importance` | `double precision` NULL | `[0,1]`, written only by `wikicluster`; seeds next ingest's priority. |
| `synced_at` | `timestamptz` | Last ingest/embed time. |

- **PK** `(page_id, chunk_index)` is **global**: a page present in both dump and crawl collapses to one
  row (last writer wins on content + provenance tag) - correct dedup, documented behavior.
- **Index** `wiki_chunks_embedding_hnsw` - HNSW `halfvec_cosine_ops`, `m=16`, `ef_construction=200`.
  HNSW skips NULL rows, so unembedded chunks cost nothing in the index.

### `wiki_sync_state` (checkpoint - dump path only)

`corpus` (PK), `dump_version`, `last_change_ts`, `synced_at`. The crawl path keeps **no checkpoint**
(re-run is safe via idempotent upserts).

---

## 8. Confidence by closeness

When a live statement is checked, `internal/service/confidence.go` aggregates its retrieved cluster into
one score - this is the "how close are we to the chunks and claims" number.

- Each **curated claim** match contributes its cosine similarity as signed evidence: a corroborating
  claim adds to Supporting, a contradicting claim adds to Contradicting, an unclear claim is ignored.
- Each **Wikipedia evidence** match contributes its similarity scaled by a chunk-kind weight (a lead
  summary outweighs buried body prose) to Supporting.
- **Score** = `Supporting / (Supporting + Contradicting)`, bounded `[0,1]`; `0` when nothing
  stance-bearing corroborates the statement.

```mermaid
flowchart LR
    Q["statement"] --> R["retrieve cluster<br/>(chunks + claims, by cosine)"]
    R --> A["computeConfidence<br/>similarity-weighted"]
    A --> S["score + supporting/contradicting<br/>+ evidence-item count"]
```

The formula is deterministic and does no extra retrieval. Surfacing the score and its
supporting/contradicting breakdown in the API/UI is **VER-77**; the formula itself is unchanged.

---

## 9. Vector consistency - the guarantees

Each guarantee maps to a concrete mechanism and holds for **both** ingestion paths.

1. **One model, one dimension, one type.** Every vector is `voyage-4-large`, 1024-dim, `halfvec(1024)`.
   `domain.EmbeddingDim = 1024` is validated on write and query. A dim/model mismatch is a bug, not a
   config option.
2. **Symmetric model, asymmetric prompt.** Ingest embeds `input_type=document`; search embeds
   `input_type=query`. Same model, correct per-side input type.
3. **Never serve a stale or half-embedded vector.** `SearchWikiChunks` filters `WHERE embedding IS NOT
   NULL` and HNSW excludes NULL rows. Dump upsert keeps an embedding only if content is byte-identical
   (else NULL); the crawl upsert writes content + vector together; both match on content so a vector is
   never attached to text it was not computed from.
4. **Growth shape.** Bulk-into-live and crawl grow one searchable chunk at a time (NULL rows invisible);
   `-atomic` swaps wholesale in one validated transaction (non-empty, zero NULL) - readers never see a
   mixture.
5. **Idempotent, at-least-once writes.** A redelivered job rewrites the same vector; a job whose row no
   longer matches is dropped. Duplicates from the keyset-paged / confirm-based publisher are harmless.
6. **No binary-COPY corruption.** Vectors are written as pgvector **text** form `[a,b,c]::halfvec`
   (`formatHalfVec`), never binary `COPY` (which corrupts `halfvec`).
7. **Verifier reports coverage, gates consistency** (`cmd/wikiverify`). It reports embedded coverage as
   progress (not a gate) and fails only on a real defect: chunks present, no zero-vector embeddings,
   dimension exactly `halfvec(1024)`, `kind IN ('lead','body')`, HNSW index present and valid. It asserts
   the whole live corpus - dump and crawl rows alike.
8. **Clustering never mutates vectors.** `wikicluster` reads vectors, runs deterministic spherical
   k-means, writes only `cluster_id` + `importance`. Idempotent.
9. **Fact-checkability is a producer-side filter, not a consistency mechanism** *(target - VER-74)*. The
   gate decides *what to publish*; it never alters a vector or a row already written. With the gate on,
   the corpus is bounded to evidence; with `CRAWL_CHECKWORTHY=false` it is the full crawl. It fails open,
   so a gate outage degrades to "more content," never to a corrupted or empty corpus.

**Honest caveats:**
- The dry-run cost estimate uses a chars/token heuristic and a fixed per-1M-token price - an estimate,
  not a billed figure.
- A per-chunk LLM gate on a large crawl is tens of thousands of Haiku calls - cheap per call, real in
  aggregate; bounded by `CRAWL_CHECKWORTHY_RPM` and `CRAWL_MAX_PAGES`.
- The gate decision is baked at crawl time; re-tuning the prompt means re-crawling. Dropped chunks are
  counted/logged, not stored.
- `prop=extracts` strips headings, so body chunks store `section=''` (real headings need `action=parse`,
  deferred). Re-crawling re-embeds unchanged articles (no producer checkpoint in v1).
- A chunk that fails every retry is dropped. In bulk-into-live/crawl it stays un-embedded; in `-atomic`
  it blocks the swap (an `ErrDrainStalled` after the stall timeout - a safety stop needing attention).

---

## 10. How to use it (local)

All commands are `make` targets (Compose under the hood). They need the dev stack and a real
`EMBEDDING_API_KEY` in `.env`. The fleets are **paid** and live behind the `wiki` Compose profile, so a
plain `make up` never starts them.

### Dump path: first-time bulk build

```bash
make up                              # postgres + migrate + offline seed (no fleet)
make fleet-up EMBEDWORKER_REPLICAS=4 # broker + N competing workers (more = faster drain, same $)

docker compose --profile tools run --rm wiki-populate \
  go run ./cmd/wikisync -mode=bulk -dry-run    # free cost estimate first

make wiki-populate                   # paid: ingest into live + publish; the fleet embeds in place
make wiki-cluster                    # optional: cluster + importance (after embedding)
make wiki-verify                     # reports embedded coverage; green = consistent
make fleet-down
```

Watch the queue at the RabbitMQ dashboard: <http://localhost:15672> (login `app` / `dev`).

### Crawl path: focused, fact-checkable slice *(target - VER-74)*

```bash
# in .env
CRAWL_CATEGORIES="Category:Climate change,Category:Vaccines"
CRAWL_MAX_PAGES=2000
CRAWL_CHECKWORTHY=true                # gate on: embed only fact-checkable content

# Option A - auto-prime: bringing up the paid profile starts the broker + worker fleet
# and runs a one-shot crawl (wiki-prime) that fills the broker; the fleet drains it.
make prime                                       # == docker compose --profile wiki up -d

# Option B - explicit producer run (different categories / re-prime):
make crawl-workers CRAWLWORKER_REPLICAS=6        # start N crawl consumers
make crawl CRAWL_CATEGORIES="Category:Physics"   # crawl + gate + publish, then exit

make wiki-verify                                 # combined corpus complete & consistent
make fleet-down
```

A plain `make up` starts nothing paid - no worker fleet, no auto-prime (the broker
container is profileless and idle; it makes no API calls). The one-shot ops tools
(`wiki-populate`, `wiki-reset`, `wiki-cluster`, `wiki-verify`, `wikicrawl`) live in the
`tools` profile and only run when invoked (`make <target>` or
`docker compose --profile tools run --rm <tool>`); they never auto-start on
`docker compose --profile wiki up`. That is what makes the bare `--profile wiki up`
auto-prime safe: it brings up only the broker, the worker fleet, and the one-shot
`wiki-prime` crawl.

### Ingesting more dump content

The volume lever is `WIKI_CORPUS`; point it at a bigger dump and force a rebuild:

```bash
# in .env: WIKI_CORPUS=enwiki   (full English; was simplewiki ~250k)
docker compose --profile tools run --rm wiki-populate \
  go run ./cmd/wikisync -mode=bulk -dry-run      # check the larger bill first
make reingest                                    # long, paid, unattended; green verify = ready
```

Valid names are Wikimedia dump names `{lang}wiki` (`enwiki`, `frwiki`, ...); non-dump names like
`frwiktionary` are rejected.

### Keeping a dump corpus fresh

```bash
make wiki-update     # delta sync: only articles changed since the checkpoint (inline embed, no swap)
```

### Key make targets

| Target | Purpose |
|--------|---------|
| `make fleet-up [EMBEDWORKER_REPLICAS=N]` | Start broker + N dump workers. |
| `make fleet-down` | Stop broker + workers (DB untouched). |
| `make wiki-populate` | Dump bulk-into-live ingest + enqueue. Add `-atomic` for stage-and-swap. |
| `make wiki-update` | Incremental dump delta sync. |
| `make wiki-cluster` | Cluster + importance-score the embedded corpus. |
| `make wiki-verify` | Assert the live corpus complete/consistent. |
| `make reingest` | reset, populate, cluster, verify. |
| `make crawl` *(target - VER-74)* | Run the `wikicrawl` producer once against `CRAWL_CATEGORIES`. |
| `make crawl-workers [CRAWLWORKER_REPLICAS=N]` *(target - VER-74)* | Start N `crawlworker` consumers. |
| `make prime` *(target - VER-74)* | Bring up broker + worker fleet + a one-shot `wiki-prime` crawl that fills the broker from `CRAWL_CATEGORIES` (`== docker compose --profile wiki up -d`). |

---

## 11. Configuration knobs

From the root `.env` (read by Compose). Defaults shown.

### Shared / dump path

| Env var | Default | What it controls |
|---------|---------|------------------|
| `WIKI_CORPUS` | `simplewiki` | Which Wikimedia dump = how much/what content. |
| `EMBEDDING_API_KEY` | - | Voyage key (required for any embed). |
| `EMBEDDING_MODEL` | `voyage-4-large` | Embedding model. Pinned to 1024-dim. |
| `EMBEDWORKER_REPLICAS` | 2 | Number of competing dump workers. Linear throughput. |
| `EMBED_WORKER_CONCURRENCY` | 4 | In-flight batches per replica. |
| `EMBED_WORKER_BATCH_SIZE` | 128 | Chunks per Voyage call (<=1000). Main throughput lever. |
| `EMBED_WORKER_BATCH_WAIT` | 200ms | How long a partial batch waits before sending. |
| `RABBITMQ_PREFETCH` | concurrency x batch | Unacked jobs held per replica. |
| `EMBED_WORKER_MAX_ATTEMPTS` | 5 | Delivery budget before a chunk is dropped. |
| `EMBED_WORKER_RPM` | 0 (unpaced) | Per-replica Voyage rate cap. |
| `WIKI_EMBED_BATCH_SIZE` | 128 | Inline (delta / atomic finalize) embed batch. |
| `WIKI_DRAIN_POLL_INTERVAL` / `_STALL_TIMEOUT` | 5s / 30m | Atomic drain poll / stall abort. |
| `WIKI_CLUSTER_K` / `_MAX_ITERS` / `_SEED` | - | k-means parameters. |
| `RABBITMQ_QUEUE` / `_QUEUE_VERSIONS` | `embedding.jobs` / `v1` | Dump queue base name and version roll. |

### Crawl path *(target - VER-74)*

| Env var | Default | What it controls |
|---------|---------|------------------|
| `CRAWL_CATEGORIES` | (required) | Comma-separated category titles, e.g. `Category:Physics`. |
| `CRAWL_PROJECT` | `WIKI_CORPUS` | Wiki project to hit + URL host (e.g. `simplewiki`). |
| `CRAWL_CORPUS` | `<project>-crawl` | Provenance tag stored in `wiki_chunks.corpus`. |
| `CRAWL_MAX_DEPTH` | 1 | Subcategory recursion depth (0 = direct pages only). |
| `CRAWL_MAX_PAGES` | 5000 | Hard cap on distinct pages collected. |
| `CRAWL_INCLUDE_BODY` | true | When false, ingest lead only. |
| `CRAWL_CHECKWORTHY` | true | Enable the producer-side fact-checkability gate. `false` publishes all. |
| `CRAWL_CHECKWORTHY_MODEL` | `claude-haiku-4-5-20251001` | Gate model. |
| `CRAWL_CHECKWORTHY_CONCURRENCY` | 8 | In-flight gate judgments in the producer. |
| `CRAWL_CHECKWORTHY_RPM` | 0 (unpaced) | Per-producer Anthropic rate cap. |
| `CHECKWORTHY_API_KEY` | - | Anthropic key for the gate. |
| `RABBITMQ_CRAWL_QUEUE` | `crawl.chunks` | Crawl queue base name (versioned via `RABBITMQ_QUEUE_VERSIONS`). |
| `CRAWL_WORKER_CONCURRENCY` | 4 | In-flight embeds per crawl worker (also prefetch). |
| `CRAWL_WORKER_MAX_ATTEMPTS` | 5 | Crawl delivery budget. |
| `CRAWLWORKER_REPLICAS` | 2 | Competing crawl worker replicas. |

---

## 12. Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `make wiki-populate` logs "already current" | Live corpus matches the dump version and is fully embedded. | Expected. Raise `WIKI_CORPUS` for more; `make reingest` to rebuild the same dump. |
| `ErrDrainStalled` after 30m (atomic only) | A chunk failed every retry, or the fleet died. | Check worker logs / RabbitMQ; confirm key + quota; re-run with `-atomic` (resumes). Old corpus keeps serving. |
| `wiki-verify` coverage below 100% | Fleet still embedding, or a few chunks dropped after retries. | Expected mid-ingest; corpus is usable. Leave the fleet running or re-publish the remainder. |
| `wiki-verify` fails on HNSW index | Index missing/invalid after a manual change. | `make reingest` rebuilds the index. |
| Workers idle while jobs sit | Fleet not up, or wrong queue version. | `make fleet-up`; confirm `RABBITMQ_QUEUE_VERSIONS` matches producer and workers. |
| Delta refuses to run | No baseline, checkpoint too old, or a bulk build in progress. | Run/finish a bulk build first, then delta. |
| Provider latency / Voyage timeouts | Provider-side, not a bug. | Tune `WIKI_EMBED_*` / `EMBED_WORKER_RPM`; do not lower defaults blindly. |
| **Crawl** publishes nothing *(VER-74)* | `CRAWL_CATEGORIES` empty/typo'd, or the gate dropped everything. | Check the category title; check the producer's published-vs-dropped counts; try `CRAWL_CHECKWORTHY=false` to isolate the gate. |
| **Crawl** corpus has obvious non-evidence prose *(VER-74)* | Gate disabled or failing open under provider errors. | Confirm `CRAWL_CHECKWORTHY=true`; check producer logs for fail-open gate errors and Anthropic key/quota. |
| **Crawl** gate is slow / expensive *(VER-74)* | Unpaced gate on a large crawl. | Lower `CRAWL_MAX_PAGES`, set `CRAWL_CHECKWORTHY_RPM`, or scope `CRAWL_CATEGORIES` tighter. |

---

## 13. Cloud / production pipeline

The local flow runs the producer and the fleet on one machine. In production the same binaries run as
separate workloads against an Amazon MQ for RabbitMQ broker; the deploy is human-gated
(`deploy-workers.yml`, `workflow_dispatch`-only). Same images, different entry points - no separate
producer or worker image.

### On-demand control: `make` targets (the operator entrypoint)

Nothing ingestion-related runs 24/7 in prod: the worker services sit at desired-count **zero** and the
one-shot ingests are tasks, not services, so idle cost between runs is zero. The operator drives a run
with AWS-CLI-backed `make` targets (`scripts/worker-fleet.sh`, `scripts/run-ingest-task.sh`) that source
the ECS cluster from terraform outputs and the subnets/security-group from the published SSM parameters
(`/<project>/<env>/deploy/*`) - never hard-coded. Everything is human-triggered; nothing schedules itself.

A typical run (prod is the default `ENV`; pass `ENV=dev` for dev):

```bash
# 1. start the embedding fleet for the run (worker services are at zero otherwise)
make worker-up FLEET=embedworker COUNT=4        # aws ecs update-service --desired-count 4
make worker-status FLEET=embedworker            # desired/running counts (read-only)

# 2. run a one-shot ingest; it launches a Fargate task, waits, and reports the exit code
make ingest-run INGEST=statsingest              # the INSEE/Eurostat/interior sweep
make ingest-run INGEST=wiki-populate            # wikisync -mode=bulk
make ingest-run INGEST=wikisync                 # wikisync -mode=delta (refresh)

# 3. scale the fleet back to ZERO when the queue drains - idle cost returns to zero
make worker-down FLEET=embedworker
```

`FLEET` is the ECS service name (`embedworker`, `crawlworker`, and the foundation
`factcheckworker`/`scrutinsworker` - the worker modules name the service by this bare suffix; only the
task-definition family carries the `<project>-<env>-` prefix). `INGEST` is `statsingest`, `wikisync`, or
`wiki-populate`, mapping to its task-definition family and command override (`wiki-populate` is the
`wikisync` family run with `-mode=bulk`; the command override targets the container by its bare name,
which is what the task definition uses - a mismatched override name is silently dropped by ECS).

Scaling uses `aws ecs update-service --desired-count` - an explicit operator count up and an explicit
zero down - exactly as the worker-lifecycle **scale** lambda's own `SetDesiredCount` does, not that
lambda (a queue-depth autoscaler for a continuously-running fleet, the opposite of this on-demand model).
The worker services run under an EXTERNAL deployment controller, so the desired count only launches tasks
once a PRIMARY task set exists; the worker-lifecycle **deploy** lambda creates it via
`scripts/deploy-ingestion.sh`. Roll the fleet once with that script before the first on-demand scale-up;
`deploy-ingestion.sh` still owns image rolls thereafter.

**Dry run without infra or credentials.** Every target honours `DRY_RUN=1`, printing the exact AWS call it
would make and skipping it, with `CLUSTER`/`SUBNETS`/`SECURITY_GROUP` overridable so the config lookups are
bypassed:

```bash
DRY_RUN=1 CLUSTER=c SUBNETS=subnet-a SECURITY_GROUP=sg-1 make ingest-run INGEST=statsingest
```

### INSEE re-run idempotency checkpoint

After a real statsingest ingest into prod RDS, prove a re-run adds no duplicate passages - the validation
of the VER-123/124 provenance-key scheme (the stable `(series, period)` key behind the
`wiki_chunks (page_id, chunk_index)` upsert) against real RDS, not only the in-memory integration test.
Run it over an open `make db-tunnel` tunnel (it `psql`s to `localhost`):

```bash
make db-tunnel                                  # terminal 1: SSM port-forward to the private RDS
PGPASSWORD=... make insee-idempotency-check     # terminal 2: count, re-run statsingest, assert no growth
```

It counts `wiki_chunks` rows whose `corpus` matches the INSEE corpora (`insee`, `insee-chomage`,
`insee-emploi`, `insee-prix`, `insee-pib`), re-runs the ingest, counts again, and exits non-zero if the
count grew. `SKIP_INGEST=1` does a back-to-back count without re-ingesting (e.g. to re-check after a
manual run); `DRY_RUN=1` dry-runs the re-ingest. The credentials ride in `PGPASSWORD`/`PGURL` in the
environment, never on an argv.

### Versioned queue

The queue is `<base>.v<version>`; `RABBITMQ_QUEUE_VERSIONS` is a comma-separated, oldest-first list
(default `1`). The newest version is active: the producer publishes to it and stamps it on every message
(an AMQP header); a worker drops a message stamped with a version it does not know. To roll, append a new
version; workers on the old version drain it, then it is removed from the list. Delivery stays
at-least-once with publisher confirms and durable, priority-ordered queues. The same machinery serves the
crawl queue.

### Dump producer task (on demand)

A deployable Fargate task runs `wikisync -mode=bulk` (bulk-into-live): ingests the dump, publishes one
self-contained, versioned job per chunk, and exits; the fleet embeds in place. For a wholesale re-chunk
cutover, run `-atomic -publish-only` and the consumer owns the drain + swap. Terraform (`enable_producer`,
off by default) creates a task definition with no schedule; launch on demand:

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

The run is resumable (keyset cursor; at-least-once with idempotent workers). The producer needs a
database (`enable_rds`, or a tunnelled local database); the fleet writes vectors back to that database.

### Crawl producer task + worker service

Mirrors the dump producer/fleet. A Fargate `wikicrawl` task (behind `enable_crawl_producer`, off by
default) fills the cloud crawl queue on demand; it reads the Anthropic gate key (`CHECKWORTHY_API_KEY`)
from Secrets Manager and does not embed, so it carries no embedding key. Unlike the dump producer it is
database-free - it crawls the MediaWiki Action API, runs the fact-checkability gate, and publishes
self-contained chunk jobs - so it is **not** bound to `enable_rds`. The `crawlworker` fleet (which embeds
and writes to the corpus) holds the embedding key. A `crawlworker` service runs under the worker-lifecycle EXTERNAL deployment
controller (scaled/deployed via the lambda, never a direct service update that drops in-flight work); its
queue-depth policy is the `crawlworker` entry in `worker_lifecycle_scaling_config` (`crawl.chunks` base,
`max = 0` until raised). The crawl queue uses the same `RABBITMQ_QUEUE_VERSIONS` machinery as the dump
queue, on its own base name `crawl.chunks` (`RABBITMQ_CRAWL_QUEUE`).

Before the first launch, populate the gate-key secret out of band (the gate is on by default), the same
way the app keys are set: `aws secretsmanager put-secret-value --secret-id truth-in-stream/dev/app/checkworthy-api-key --secret-string <anthropic-key>`
(or set `CRAWL_CHECKWORTHY=false` in the run override to skip the gate). The task definition carries a
default `CRAWL_CATEGORIES`; override it per run to crawl a different slice. The launch is the same
`aws ecs run-task` shape against `truth-in-stream-<env>-wikicrawl`:

```bash
cd stack/terraform/dev
SUBNETS=$(aws ssm get-parameter --name /truth-in-stream/dev/deploy/private-subnet-ids --query Parameter.Value --output text)
SG=$(aws ssm get-parameter --name /truth-in-stream/dev/deploy/tasks-security-group-id --query Parameter.Value --output text)
aws ecs run-task \
  --cluster "$(terraform output -raw ecs_cluster_name)" \
  --task-definition truth-in-stream-dev-wikicrawl \
  --launch-type FARGATE \
  --network-configuration "awsvpcConfiguration={subnets=[$SUBNETS],securityGroups=[$SG],assignPublicIp=DISABLED}" \
  --overrides '{"containerOverrides":[{"name":"wikicrawl","environment":[
      {"name":"CRAWL_CATEGORIES","value":"Category:Physics,Category:Vaccines"},
      {"name":"CRAWL_MAX_PAGES","value":"2000"}]}]}'
```

The producer keeps no checkpoint; a crash just re-runs (idempotent upserts make a re-crawl harmless).

### Deploy the producer and worker (CI)

After the backend/frontend services roll, `scripts/deploy-ingestion.sh` ships the deployed image
(immutable `sha-<short>` tag, never `:latest`) to the ingestion workloads:

- **Producer(s):** register a new task-definition revision pinned to that image, so the next on-demand
  `run-task` uses the exact built, Trivy-scanned image.
- **Worker fleet(s):** invoke the worker-lifecycle **deploy** lambda with the image and worker service
  names. The lambda creates and promotes a new task set under the EXTERNAL controller, so in-flight
  messages drain on the old task set before it is retired. The workflow never updates a worker service
  directly.

A workload not provisioned yet is skipped, not fatal. By default the script ships to both producers
(`producer`, `wikicrawl`) and both worker fleets (`embedworker`, `crawlworker`); each crawl workload is
skipped individually when absent, so the deploy is safe before the crawl path is stood up.
`scripts/deploy-ingestion.test.sh` covers the crawl producer/worker skip paths. Override `PRODUCER_SERVICES`
/ `WORKER_SERVICES` to target a subset.

**Rolling the queue version** is an explicit, gated step in the same deploy: run `deploy-workers.yml`
with `queue_versions` set to the new oldest-first list. The version lives on the task definitions, so the
roll never edits Terraform.

### Drain the cloud queue locally (SSM bastion tunnel)

The develop-locally model keeps data on your machine: the producer fills the cloud queue, then you run
the worker locally against a tunnel so it drains into local Postgres. No cloud database is involved. The
broker is private (AMQPS 5671, VPC-only), so the tunnel goes through a hardened SSM-only bastion (no SSH,
no public IP, IMDSv2 required).

```bash
cd stack/terraform/dev
terraform apply -var enable_bastion=true   # deploy is human-gated; run with elevated creds

aws sso login --profile verovec-dev
./scripts/ssm-port-forward.sh dev          # prints localhost:5671; keep it open
```

The broker speaks AMQPS with a certificate for its real hostname:

```bash
BROKER_URL=$(aws secretsmanager get-secret-value --secret-id truth-in-stream/dev/rabbitmq/url \
  --query SecretString --output text --profile verovec-dev)
BROKER_HOST=$(printf '%s' "$BROKER_URL" | sed -E 's#.*@([^:/]+).*#\1#')
echo "127.0.0.1 $BROKER_HOST" | sudo tee -a /etc/hosts   # remove when done
```

In a second terminal, run the worker as a host process (not the compose worker, which is on the container
network and cannot reach the host tunnel) against the tunnel and local database:

```bash
cd stack/backend
RABBITMQ_URL="$BROKER_URL" \
DATABASE_URL='postgres://postgres:dev@localhost:5432/truthinstream?sslmode=disable' \
EMBEDDING_API_KEY="$EMBEDDING_API_KEY" \
  go run ./cmd/embedworker        # or ./cmd/crawlworker to drain the crawl queue (crawl.chunks)
```

`./cmd/crawlworker` reads the same three env vars (it shares the broker URL, embedding key, and local
database); it binds to `crawl.chunks` instead of `embedding.jobs`, so the crawl producer's cloud queue
drains straight into local Postgres exactly as the dump worker does.

Prerequisites: AWS CLI v2 and the Session Manager plugin. The SSM port-forward has no TCP keepalive, so
an idle tunnel can drop - re-run the script to reconnect. When done, drop the `/etc/hosts` line and tear
the bastion down (`terraform apply -var enable_bastion=false`).

---

## 11. Confidence by closeness (query-time scoring)

The pipeline above builds the evidence corpus; this section is what a *query* does with it. It is
**query-time, not ingest-time**: nothing here writes to `wiki_chunks` or `claims`, and the score
is streamed on the live result frame, never stored.

When a spoken statement is matched, its retrieved cluster is aggregated into a single
**confidence score** - how strongly the corpus corroborates the statement, by the closeness of
its matches. The formula lives in exactly one place, `internal/service/confidence.go`
(`computeConfidence`), and is bounded by the matcher config (`ConfidenceClusterSize`,
`ConfidenceLeadWeight`, `ConfidenceBodyWeight`).

- Each **curated claim** match contributes its cosine similarity as signed evidence: a
  corroborating claim adds to **Supporting**, a contradicting claim adds to **Contradicting**, an
  unclear claim is ignored.
- Each **Wikipedia evidence** match contributes its similarity scaled by a chunk-kind weight (a
  `lead` summary outweighs buried `body` prose) to **Supporting**.
- Only the strongest `ConfidenceClusterSize` matches feed the score.
- **Score** = `Supporting / (Supporting + Contradicting)`, bounded `[0, 1]`; `0` when nothing
  stance-bearing corroborates the statement.

The live result frame surfaces the score *and* its breakdown so it is explainable: `confidence`
carries `{ score, supporting, contradicting, evidence_items }`, and every match carries its own
`contribution` (the stance-bearing weight it added; `0` for an unclear claim, a non-positive
similarity, or a match beyond the cluster cap). The per-match contributions sum to
`supporting + contradicting`. The frontend renders the percentage with a compact
supporting/contradicting breakdown beneath it. See the data dictionary for the field-level
reference.

---

## 12. Category-crawl ingestion (additive)

A second, **additive** path fills `wiki_chunks` from a focused category slice over
HTTP - no multi-gigabyte dump download. It never touches the dump pipeline
(`wikisync`/`embedworker`) or its staging table; it upserts straight into live
`wiki_chunks` alongside whatever the dump pipeline already wrote.

```mermaid
flowchart LR
    CAT["Wikipedia<br/>Action API"]
    subgraph PROD["cmd/wikicrawl (producer, DB-free, + LLM gate)"]
      M["list=categorymembers<br/>BFS subcats to depth, page cap"]
      X["prop=extracts<br/>lead (exintro) + full (explaintext)"]
      C["Chunk() lead -> kind=lead<br/>body  -> kind=body"]
      G["fact-checkability gate<br/>internal/evidencegate: citable<br/>evidence? drop if not (fail-open)"]
      P["publish 1 self-contained<br/>CrawlJob per PASSING chunk"]
    end
    Q[["RabbitMQ<br/>crawl.chunks.v&lt;n&gt;<br/>durable, priority"]]
    subgraph FLEET["cmd/crawlworker x N (competing consumers)"]
      W1["worker 1"]
      WN["worker N"]
    end
    V["Voyage voyage-4-large<br/>input_type=document"]
    LIVE[("wiki_chunks (live)<br/>+ HNSW index")]

    CAT --> M --> X --> C --> G --> P --> Q
    Q --> W1 & WN
    W1 & WN --> V
    W1 & WN -->|"UpsertEmbeddedChunk<br/>(content + vector, atomic)"| LIVE
```

| Piece | Location | Responsibility |
|---|---|---|
| Category crawler | `internal/wiki/crawl.go` | `CategoryMembers` BFS over the Action API: subcategories to `MaxDepth`, dedupe page ids, stop at `MaxPages`, follow continuation. |
| Body extracts | `internal/wiki/mediawiki.go` | `FullExtracts` (`explaintext`, no `exintro`) for the article body; the lead source `Extracts` is unchanged. |
| Producer logic | `internal/wiki/crawlproduce.go` | `RunCrawl`: crawl, fetch lead + body, chunk, gate each chunk, publish one `CrawlJob` per *passing* chunk (lead chunks then body, contiguous `chunk_index`). Transport-free, DB-free. |
| Fact-checkability gate | `internal/evidencegate/evidencegate.go` | Per-chunk LLM judgment: *"is this passage verifiable, citable factual evidence?"* A forced-tool, temperature-0 Haiku call on `internal/llm`. Distinct from `internal/checkworthy` (which judges a short spoken statement). The producer drops chunks it rejects before they reach the broker; on a gate error the chunk is published anyway (fail-open). |
| Job + consumer | `internal/crawljob/crawljob.go` | The self-contained `CrawlJob` message and the `Worker` that embeds each chunk then upserts it live; mirrors `internal/embedjob` semantics. |
| Producer binary | `cmd/wikicrawl/` | Wire config + API client + broker; run `RunCrawl`; exit. No DB connection. |
| Consumer binary | `cmd/crawlworker/` | Wire config + Voyage embedder + store + broker; run the worker until SIGTERM. |
| Store write | `queries/wiki.sql` (`UpsertEmbeddedChunk`), `internal/store/postgres/wiki.go` | Insert-or-replace content **and** embedding in one statement (text-form `::halfvec`, never binary COPY), so a row is never searchable without its vector. |

**Self-contained messages.** Every field a live `wiki_chunks` row needs travels in
the `CrawlJob` body, so the worker reads nothing from the database before writing
and the broker can hold a primed corpus indefinitely before any worker is started -
the convenience the path exists for.

**Provenance and PK.** Crawl rows carry `corpus = CRAWL_CORPUS` (default
`<project>-crawl`), distinct from the dump corpus, so a dump-corpus delta never
touches crawl rows and vice versa. The `(page_id, chunk_index)` primary key is
**global**, so a page present in both collapses to one row (last writer wins on
content + provenance) - intended dedup.

**Error handling.** The worker mirrors `embedjob`: malformed / invalid / unknown
queue-version messages are ack-dropped; a bad provider shape (not 1x1024) is
dropped; a transient embed/upsert failure is re-enqueued with the attempt
incremented and dropped after `CRAWL_WORKER_MAX_ATTEMPTS`; a shutdown mid-work
nacks for requeue. Vector consistency is identical to the dump pipeline (one
model, 1024 dims, `halfvec`, `input_type=document`), so `make wiki-verify` asserts
the combined corpus.

**Fact-checkability gate (producer-side).** With `CRAWL_CHECKWORTHY=true` (the
default), the producer runs a per-chunk LLM judgment after chunking and before
publishing, with up to `CRAWL_CHECKWORTHY_CONCURRENCY` calls in flight and the
rate capped by `CRAWL_CHECKWORTHY_RPM` (0 = unpaced). A chunk judged not citable
evidence is **dropped** (never published), so the broker and all downstream
embedding spend are bounded to fact-checkable content; dropped vs published is
counted and logged per batch and at the end of the run. The gate **fails open**:
on any LLM error the chunk is published anyway, so a flaky model can never
silently empty the corpus. The decision is baked at crawl time - re-tuning the
prompt means re-crawling. Set `CRAWL_CHECKWORTHY=false` to publish every chunk
(the pre-gate behavior); when on, the producer requires `CHECKWORTHY_API_KEY` and
fails fast without it. The gate runs **only** in the producer; the worker and the
live `internal/checkworthy` path are untouched.

### Configuration

| Env var | Default | Controls |
|---|---|---|
| `CRAWL_CATEGORIES` | (required for producer) | Comma-separated category titles, e.g. `Category:Climate change,Category:Physics`. |
| `CRAWL_PROJECT` | `WIKI_CORPUS` value | Wiki project queried and used to build article URLs (e.g. `simplewiki`). |
| `CRAWL_CORPUS` | `<project>-crawl` | Provenance tag stored in `wiki_chunks.corpus`. |
| `CRAWL_MAX_DEPTH` | `1` | Subcategory recursion depth (0 = direct pages only). |
| `CRAWL_MAX_PAGES` | `5000` | Hard cap on distinct pages collected. |
| `CRAWL_INCLUDE_BODY` | `true` | When false, ingest lead only (`kind='lead'`). |
| `CRAWL_CHECKWORTHY` | `true` | Producer-side fact-checkability gate. `false` publishes every chunk (pre-gate behavior). |
| `CHECKWORTHY_API_KEY` | (required when gate on) | Anthropic key for the gate; read from env, never logged. |
| `CRAWL_CHECKWORTHY_MODEL` | `claude-haiku-4-5-20251001` | Gate model (cheapest fast Claude). |
| `CRAWL_CHECKWORTHY_CONCURRENCY` | `8` | In-flight gate judgments in the producer. |
| `CRAWL_CHECKWORTHY_RPM` | `0` (unpaced) | Per-producer Anthropic call-rate cap. |
| `RABBITMQ_CRAWL_QUEUE` | `crawl.chunks` | Base queue name; versioned via `RABBITMQ_QUEUE_VERSIONS`. |
| `CRAWL_WORKER_CONCURRENCY` | `4` | In-flight embeds per worker replica; also the prefetch. |
| `CRAWL_WORKER_MAX_ATTEMPTS` | `5` | Delivery budget before a job is dropped. |
| `CRAWLWORKER_REPLICAS` | `2` | Competing worker replicas (`make crawl-workers`). |

The embedding key/model reuse `EMBEDDING_API_KEY` / `EMBEDDING_MODEL`; the broker URL reuses `RABBITMQ_URL`.

### How to run

Behind the paid `wiki` Compose profile (a plain `make up` starts no worker fleet and
no auto-prime; the profileless broker idles, making no API calls):

```bash
# Auto-prime: bring up the broker + worker fleet + a one-shot crawl that fills the
# broker from CRAWL_CATEGORIES (set it and the gate key in .env first). The fleet
# drains it as it fills.
make prime                                            # == docker compose --profile wiki up -d

# Or run the producer explicitly (re-prime / different categories):
make crawl-workers CRAWLWORKER_REPLICAS=4              # start N crawl consumers
make crawl CRAWL_CATEGORIES="Category:Climate change" CRAWL_MAX_PAGES=2000
                                                       # crawl + gate + publish, then exit
make wiki-verify                                       # corpus (dump + crawl) complete & consistent
```

`wiki-prime` (the auto-prime service) and the worker fleet (`embedworker`,
`crawlworker`) live in the `wiki` profile, so `docker compose --profile wiki up -d`
brings up exactly broker + fleet + a one-shot prime crawl. The one-shot ops tools
(`wikicrawl` on-demand, `wiki-populate`, `wiki-reset`, `wiki-cluster`, `wiki-verify`)
live in the separate `tools` profile and only run when invoked, never on `up` -
that is what keeps the bare `--profile wiki up` auto-prime safe. `wiki-prime` requires
`CRAWL_CATEGORIES` (it fails fast without it) and, with the gate on by default, a
`CHECKWORTHY_API_KEY` (or `CRAWL_CHECKWORTHY=false`).

### Known limitations (v1)

Body chunks store `section=''` (extracts strip heading markup); the producer keeps
no checkpoint, so a crash means re-run (safe via idempotent upserts); a re-crawl
re-embeds unchanged articles. See the design for the rationale and follow-ups:
`docs/superpowers/specs/2026-06-15-wikipedia-category-crawl-ingestion-design.md` SS11.

---

## 13. Cross-references

- Data dictionary: `.claude/skills/data-map/SKILL.md`
- Design specs: `docs/superpowers/specs/2026-06-10-wikipedia-ingestion-design.md`,
  `docs/superpowers/specs/2026-06-11-wiki-ingest-staging-redesign-design.md`,
  `docs/superpowers/specs/2026-06-15-wikipedia-category-crawl-ingestion-design.md`
- Schema: `stack/backend/migrations/0004_wiki_chunks.up.sql`, `0009_*`, `0010_*`
- Queries: `stack/backend/queries/wiki.sql`
- Confidence scoring (query-time): `stack/backend/internal/service/confidence.go`, `match.go`
- LLM classifiers: `stack/backend/internal/llm` (shared transport), `internal/evidencegate` (crawl gate), `internal/checkworthy` (live)
- Commands: `stack/backend/cmd/{wikisync,embedworker,wikicluster,wikiverify,wikicrawl,crawlworker}/`
- Cloud deploy: `scripts/deploy-ingestion.sh`, `scripts/ssm-port-forward.sh`, `.github/workflows/deploy-workers.yml` (calls the reusable `_deploy.yml`)
- On-demand control: `scripts/worker-fleet.sh` (scale up/down/status), `scripts/run-ingest-task.sh` (one-shot ingest), `scripts/insee-idempotency-check.sh`, `scripts/ingestion-common.sh` (shared config), make targets `worker-up`/`worker-down`/`worker-status`/`ingest-run`/`insee-idempotency-check`
- Infra: `stack/terraform/README.md` (`enable_producer`, `enable_bastion`, `enable_rds`, the `rabbitmq` module)
