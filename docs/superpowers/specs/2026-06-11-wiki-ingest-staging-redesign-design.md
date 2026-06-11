# Wiki ingest pipeline redesign: staging as the materialized next corpus

Date: 2026-06-11
Branch: `wiki-ingest-staging-redesign` (off `main`)
Supersedes: commit `57a4114` "fix(wiki): discard stale staging when the dump version changed"

## Problem

`make wiki-populate` fails at the swap step:

```
ingest complete ............ chunks 279343
starting bulk embed ........ pending_chunks 0, resume_after_page 1269879
all pending chunks embedded  embedded_this_run 0
wikisync failed ............ wiki: finalize staging: postgres: swap staging into place:
                             staging has 279343 chunks, live has 279350; refusing to swap a partial corpus
```

Two independent defects, both tripping the same `staging == live` guard:

1. **Orphan accumulation in live.** Ingest writes directly into the live `wiki_chunks`
   table (`internal/wiki/sync.go` `UpsertWikiChunk` + per-page `TrimPages`). Trim only
   runs for pages present in the current dump, so when a page disappears between dumps
   (deleted, or demoted to a redirect/disambiguation no longer in the stream) its old
   chunks are never removed. Live drifts above the current dump's true count (279350 vs
   279343 here).

2. **The staging model only reconciles a fresh corpus.** The embed loop
   (`internal/store/postgres/wiki_embed.go` `UnembeddedChunks`) reads live rows
   `WHERE embedding IS NULL`, embeds them, and copies *only those* into staging. Rows whose
   embeddings `UpsertWikiChunk`'s `CASE` carried forward are never copied into staging, so
   on any second dump `staging < live` by construction. The "discard stale staging" commit
   forces a from-scratch re-embed, which only reconciles when live is 100% NULL (a
   brand-new database).

The root cause is that ingest mutates the production serving table in place while a second
phase reconstructs a partial staging copy, and the swap tries to reconcile two things that
drift. For a staging/swap design, live should be immutable until the atomic swap.

## Goals

- Eliminate the orphan/drift class of failure entirely (not patch this instance).
- Keep incremental embedding: a new dump re-embeds only changed and new chunks, never the
  whole ~279k-chunk corpus, preserving Voyage spend and time.
- Keep `WIKI_MAX_DURATION` resume cheap and crash-safe.
- Leave live serving the previous corpus, fully embedded, until the atomic swap.

## Non-goals

- Delta mode (`-mode=delta`) is unchanged. Its `EmbedInProgress`/`stagingExists` "refuse
  while a bulk build is in flight" contract is preserved.
- No change to chunking, lead extraction, the Voyage client, retry/rate-limit decorators,
  the HNSW parameters, or the swap rename sequence itself.

## Design

### Core inversion

Ingest and embed both build `wiki_chunks_staging`. Live `wiki_chunks` is read-only until
the swap. Staging is the fully materialized *next* corpus, embeddings included.

### Build (per dump version V)

1. Create an empty, unindexed staging table; stamp it `building:V` (table-comment stamp,
   reusing the existing `stampStaging` mechanism with a structured value).
2. **Ingest the dump into staging**, not live: chunk every current page, insert rows with
   `embedding NULL`. Staging starts empty and only current-dump pages are inserted, so
   **orphans are impossible by construction** — there is no delete-absent step and no way
   for a vanished page's chunks to survive.
3. **Carry embeddings forward** with a single join:

   ```sql
   UPDATE wiki_chunks_staging s
      SET embedding = l.embedding
     FROM wiki_chunks l
    WHERE s.page_id = l.page_id
      AND s.chunk_index = l.chunk_index
      AND s.content = l.content
      AND l.embedding IS NOT NULL;
   ```

   This replaces the per-row `CASE` in `UpsertWikiChunk` as the incremental mechanism:
   unchanged chunks keep their vector; changed and new chunks stay `NULL` and get embedded.
4. Re-stamp staging `ready:V`.

### Embed (in place, on staging)

Loop until no `NULL` embeddings remain:

```sql
SELECT page_id, chunk_index, content
  FROM wiki_chunks_staging
 WHERE embedding IS NULL
 ORDER BY page_id, chunk_index
 LIMIT :superBatch;
```

Embed the batch, then write vectors back by COPYing them into a temp table in text format
and joining:

```sql
UPDATE wiki_chunks_staging s
   SET embedding = t.embedding
  FROM <temp> t
 WHERE s.page_id = t.page_id AND s.chunk_index = t.chunk_index;
```

Text-format COPY for the half-vectors sidesteps the documented pgx binary `halfvec`
corruption. `WHERE embedding IS NULL` *is* the resume cursor — the keyset watermark
(`EmbedWatermark`, `UnembeddedWikiChunks`, `EstimateRemainingWikiChunks` against live) is
retired. A `max-duration` stop leaves a committed prefix of embedded rows; the next run
re-queries the remaining `NULL`s.

### Finalize and swap

When zero `NULL` embeddings remain: build the HNSW index on staging and swap it into
`wiki_chunks` via the existing rename sequence. The swap-time guard becomes:

- staging is non-empty, **and**
- staging has zero `NULL` embeddings.

The `staging == live` count comparison is **deleted** — staging is the authority, live is
whatever it was. On swap, record `dump_version = V` in `wiki_sync_state` (column already
exists) inside the swap transaction.

### Run-start state machine

Keyed on staging presence and stamp, evaluated after the dump version V is resolved:

| Staging state | Action |
|---|---|
| absent, `wiki_sync_state.dump_version == V`, live has 0 `NULL` embeddings | No-op: "corpus already current", exit before ingest. |
| absent (otherwise) | Full build for V (steps 1–4), then embed, then swap. |
| `ready:V` | Resume: skip ingest and carry-forward entirely; embed remaining `NULL`s; swap. |
| `building:*`, or `ready:S` with S ≠ V | Interrupted build or stale dump: drop staging, full build for V. |

This makes resume cheap (no re-parse of the ~40s bz2 pass) and crash-safe: a build is
trusted only once re-stamped `ready:V`. An interrupt during ingest/carry-forward leaves
`building:V`, which the next run discards and rebuilds.

### dry-run

`-dry-run` builds staging (steps 1–4) and counts remaining `NULL`s for the cost estimate,
without embedding or swapping. It leaves a valid `ready:V` staging table that a subsequent
real run resumes from. This is strictly better than today's dry-run, which mutates live.

## Affected code

- `internal/wiki/sync.go` — ingest targets staging; drop the live upsert/trim path.
- `internal/wiki/embedrun.go` — embed-in-place loop; run-start state machine; remove
  watermark-based resume.
- `internal/store/postgres/wiki_embed.go` — new: seed-empty/create-staging with structured
  stamp, ingest-into-staging DML, carry-forward join, select-unembedded-from-staging,
  update-embeddings-from-temp, two-phase stamp read/write, `RecordDumpVersion`,
  short-circuit check. Rewrite `validateStagingTx` (drop count comparison). Retire
  `EmbedWatermark`, `UnembeddedChunks`, `EstimateRemaining` against live, `CopyStagingChunks`
  (insert form).
- `queries/wiki.sql` — staging-targeted DML; retire `UnembeddedWikiChunks` and
  `EstimateRemainingWikiChunks`; add carry-forward, select-unembedded-staging, dump-version
  upsert.
- `cmd/wikisync/main.go` — wire the no-op short-circuit and the changed estimate path; no
  flag changes.

The discard-stale-staging logic from commit `57a4114` is absorbed into the stamp state
machine; that approach is superseded.

## Error handling

- A build interrupted mid-ingest/carry-forward leaves `building:V`; the next run discards
  and rebuilds — no partial corpus is ever embeddable.
- An interrupted embed leaves `ready:V` with some `NULL`s; the next run resumes.
- `max-duration` / SIGINT cancellation continues to classify as a clean resumable stop via
  the existing `classifyStop` path.
- The swap remains a single transaction; readers see the old corpus until commit and the
  new one after.

## Testing

- `internal/wiki` — table-driven tests over a fake `EmbedStore` for every run-start
  transition: fresh build, `ready:V` resume, `building:V` discard-and-rebuild, `ready:S`
  discard, and the `dump_version == V` no-op short-circuit.
- Carry-forward unit test: unchanged content keeps its vector (not re-embedded), changed
  content is re-embedded, a new chunk is embedded, a vanished page leaves no row.
- `internal/store/postgres` — postgres-backed test (following the existing store test
  pattern) proving an orphan row seeded into live cannot appear in the swapped corpus, and
  that the swap guard rejects a staging table with any `NULL` embedding.
- `go test -race ./...`, `gofumpt`, `go vet`, `golangci-lint run ./...` green.

## Risks / trade-offs

- The carry-forward join and the embed write-back join each touch up to ~280k rows; on the
  local Postgres this is well within budget. The build is not one giant transaction (per the
  two-phase-stamp decision), so lock duration stays bounded.
- A first run of the new code against an existing live table that holds orphans heals
  automatically: staging is built only from the current dump, orphans are never copied in,
  and the swap replaces live wholesale.
