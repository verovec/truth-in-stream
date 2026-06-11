# Wiki ingest-into-staging redesign - Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `make wiki-populate` build the new corpus in `wiki_chunks_staging` (live immutable until the atomic swap), carry embeddings forward from live by content match, embed in place on staging, and swap with a guard that no longer compares staging to live - removing the orphan/drift failure class.

**Architecture:** Staging is the materialized next corpus. Ingest inserts current-dump pages into staging with NULL embeddings; a single join carries unchanged embeddings forward from live; the embed loop fills the remaining NULLs in place; finalize builds the HNSW index and swaps. A two-phase table-comment stamp (`building:V` then `ready:V`) makes `WIKI_MAX_DURATION` resume cheap and crash-safe. Delta mode and all live-targeting queries are untouched; every new staging operation is raw pgx (sqlc cannot model the transient table), so there is no sqlc regeneration.

**Tech Stack:** Go (stdlib + pgx/v5 + pgvector-go), Postgres 16 + pgvector (`halfvec(1024)`, HNSW), Voyage embeddings. Integration tests run against `TEST_DATABASE_URL`.

**Run integration tests with:**
```bash
cd stack/backend && TEST_DATABASE_URL='postgres://postgres:dev@localhost:5432/truthinstream?sslmode=disable' ~/sdk/go1.26.4/bin/go test -race ./internal/store/postgres/ -run Wiki
```
Unit tests (`internal/wiki`) need no DB.

---

## Design decisions locked

- **Checkpoint moves to swap.** `wiki_sync_state.dump_version` / `last_change_ts` are written only inside the swap transaction (`UpsertWikiSyncState`). Ingest no longer checkpoints. This makes `dump_version == V` an unambiguous "V is live and embedded" marker for the no-op short-circuit, and stops the delta checkpoint advancing to a corpus that is not yet live.
- **Stamp grammar:** the staging table comment is `building:<version>` during ingest/carry-forward and `ready:<version>` once materialized. An empty/legacy comment is treated as `building:""` (rebuild).
- **No delete-absent step:** staging is built only from the current dump, so orphans cannot enter it.
- **No sqlc change, no migration.** New staging ops are raw SQL. `UpsertWikiSyncState`, `CountWikiChunks` already exist.

## Store surface (new, raw SQL - in `internal/store/postgres/wiki_embed.go`)

| Method | Purpose |
|---|---|
| `StagingPlan(ctx, version)` | Returns `PlanAlreadyCurrent` / `PlanResumeEmbed` / `PlanBuild`. |
| `ResetStaging(ctx, version)` | Drop any staging, create empty unindexed staging, stamp `building:version`. |
| `UpsertStagingChunks(ctx, chunks)` | Batch insert chunks (NULL embedding) into staging. |
| `CarryForwardEmbeddings(ctx)` | `UPDATE staging SET embedding FROM live WHERE pk match AND content match`; returns rows carried. |
| `MarkStagingReady(ctx, version)` | Re-stamp `ready:version`. |
| `StagingRemaining(ctx)` | Count/chars/pages of staging rows `WHERE embedding IS NULL`. |
| `UnembeddedStaging(ctx, limit)` | Next `limit` staging rows `WHERE embedding IS NULL ORDER BY page_id, chunk_index`. |
| `UpdateStagingEmbeddings(ctx, chunks)` | COPY embeddings into a temp table (text halfvec) then `UPDATE staging FROM temp`. |
| `FinalizeStaging(ctx, corpus, version, lastChangeTS, mem, workers)` | Build index, then in one tx: guard (non-empty + 0 NULL), rename swap, `UpsertWikiSyncState`. |

`BulkPlan` is a new exported type in `internal/wiki`. Retire the now-unused `EmbedWatermark`, `UnembeddedChunks`, `EstimateRemaining`, `CopyStagingChunks`, `DiscardStagingIfStale`, `CreateStaging` after the new path is in.

---

## Task 1: Stamp grammar helpers + `StagingPlan`

**Files:** Modify `internal/store/postgres/wiki_embed.go`, `internal/wiki/embedrun.go`; Test `internal/store/postgres/wiki_embed_test.go`.

- [ ] **Step 1: Write failing tests** for the plan classifier and stamp round-trip (`TestStagingPlan`, `TestStagingPlanAlreadyCurrent`): empty -> `PlanBuild`; `ready:v1` staging -> `PlanResumeEmbed` at v1 and `PlanBuild` at v2; `building:v1` -> `PlanBuild`; live checkpointed at v1 with zero NULLs and no staging -> `PlanAlreadyCurrent`. Reuse `chunk`/`withEmbedding`/`unitVec`/`seedChunks` helpers if present; add a test-only `markLiveEmbeddedAt` calling `UpsertWikiSyncState`.
- [ ] **Step 2: Run, expect FAIL** (undefined `StagingPlan`, `wiki.PlanBuild`).
- [ ] **Step 3: Implement** the `BulkPlan` enum in `embedrun.go`:

```go
type BulkPlan int

const (
	PlanBuild BulkPlan = iota // build staging from the dump, then embed and swap
	PlanResumeEmbed           // staging is materialized for this dump; embed remaining and swap
	PlanAlreadyCurrent        // live already serves this dump fully embedded; nothing to do
)
```

and in `wiki_embed.go`:

```go
const (
	stampBuilding = "building"
	stampReady    = "ready"
)

func stagingStamp(phase, version string) string { return phase + ":" + version }

func (s *Store) readStaging(ctx context.Context) (exists bool, phase, version string, err error) {
	var raw *string
	if err = s.pool.QueryRow(ctx,
		"SELECT to_regclass($1) IS NOT NULL, obj_description(to_regclass($1), 'pg_class')",
		wikiStagingTable,
	).Scan(&exists, &raw); err != nil {
		return false, "", "", fmt.Errorf("postgres: read staging stamp: %w", err)
	}
	if !exists || raw == nil {
		return exists, stampBuilding, "", nil
	}
	if p, v, ok := strings.Cut(*raw, ":"); ok {
		return true, p, v, nil
	}
	return true, stampBuilding, "", nil
}

func (s *Store) StagingPlan(ctx context.Context, version string) (wiki.BulkPlan, error) {
	exists, phase, stampVersion, err := s.readStaging(ctx)
	if err != nil {
		return 0, err
	}
	if exists {
		if phase == stampReady && stampVersion == version {
			return wiki.PlanResumeEmbed, nil
		}
		return wiki.PlanBuild, nil
	}
	current, err := s.liveCurrentAt(ctx, version)
	if err != nil {
		return 0, err
	}
	if current {
		return wiki.PlanAlreadyCurrent, nil
	}
	return wiki.PlanBuild, nil
}

func (s *Store) liveCurrentAt(ctx context.Context, version string) (bool, error) {
	var (
		stored *string
		nulls  int64
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT dump_version FROM wiki_sync_state LIMIT 1),
		       (SELECT count(*) FROM wiki_chunks WHERE embedding IS NULL)`,
	).Scan(&stored, &nulls); err != nil {
		return false, fmt.Errorf("postgres: read live currency: %w", err)
	}
	return stored != nil && *stored == version && nulls == 0, nil
}
```

Add `strings` import if absent. `postgres` already imports `wiki`, so `wiki.BulkPlan` is fine.

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** `feat(wiki): staging plan classifier and two-phase stamp`.

---

## Task 2: `ResetStaging`, `MarkStagingReady`, `UpsertStagingChunks`

**Files:** `wiki_embed.go`, `wiki_embed_test.go`.

- [ ] **Step 1: Failing test `TestStagingBuild`** - reset creates empty `building:V` staging; upsert inserts 3 NULL-embedding rows over 2 pages; `StagingRemaining` reports 3/2; `MarkStagingReady` flips phase to `ready`; a second `ResetStaging` drops the prior table (remaining 0).
- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Implement** (plain INSERT - staging starts empty, page ids unique, PK added at finalize, so no `ON CONFLICT` needed):

```go
func (s *Store) ResetStaging(ctx context.Context, version string) error {
	if _, err := s.pool.Exec(ctx, "DROP TABLE IF EXISTS "+wikiStagingTable); err != nil {
		return fmt.Errorf("postgres: drop staging: %w", err)
	}
	stmt := fmt.Sprintf("CREATE TABLE %s (LIKE wiki_chunks INCLUDING DEFAULTS)", wikiStagingTable)
	if _, err := s.pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("postgres: create staging: %w", err)
	}
	return s.stampStaging(ctx, stagingStamp(stampBuilding, version))
}

func (s *Store) MarkStagingReady(ctx context.Context, version string) error {
	return s.stampStaging(ctx, stagingStamp(stampReady, version))
}

func (s *Store) UpsertStagingChunks(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	stmt := fmt.Sprintf(`INSERT INTO %s (page_id, chunk_index, title, url, revision_id, corpus, content)
VALUES ($1,$2,$3,$4,$5,$6,$7)`, wikiStagingTable)
	batch := &pgx.Batch{}
	for _, c := range chunks {
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: stage chunk page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
		}
		batch.Queue(stmt, c.PageID, int32(c.ChunkIndex), c.Title, c.URL, c.RevisionID, c.Corpus, c.Content)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range chunks {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: upsert staging chunks: %w", err)
		}
	}
	return nil
}
```

`stampStaging` already exists; it sets the table comment via server-side `format(%L)`.

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** `feat(wiki): build staging from the dump (reset/upsert/ready)`.

---

## Task 3: `CarryForwardEmbeddings`

**Files:** `wiki_embed.go`, `wiki_embed_test.go`.

- [ ] **Step 1: Failing test `TestCarryForwardEmbeddings`** - live has page1/0 "v1", page1/1 "v2", orphan page9/0 "v9" (all embedded); staging (new dump) has page1/0 "v1" (unchanged), page1/1 "v2-changed", page2/0 "v7" (new). After carry-forward: returns 1; `StagingRemaining` = 2; page9 has 0 rows in staging.
- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Implement.**

```go
func (s *Store) CarryForwardEmbeddings(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s s SET embedding = l.embedding
		FROM wiki_chunks l
		WHERE s.page_id = l.page_id
		  AND s.chunk_index = l.chunk_index
		  AND s.content = l.content
		  AND l.embedding IS NOT NULL`, wikiStagingTable))
	if err != nil {
		return 0, fmt.Errorf("postgres: carry forward embeddings: %w", err)
	}
	return tag.RowsAffected(), nil
}
```

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** `feat(wiki): carry unchanged embeddings forward into staging`.

---

## Task 4: `StagingRemaining`, `UnembeddedStaging`, `UpdateStagingEmbeddings`

**Files:** `wiki_embed.go`, `wiki_embed_test.go`.

- [ ] **Step 1: Failing test `TestStagingEmbedInPlace`** - 3 NULL rows; `UnembeddedStaging(2)` returns pages 1,2 in keyset order; embed them and `UpdateStagingEmbeddings`; `StagingRemaining` drops to 1.
- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Implement.**

```go
func (s *Store) StagingRemaining(ctx context.Context) (domain.WikiRemaining, error) {
	var r domain.WikiRemaining
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*)::bigint, count(DISTINCT page_id)::bigint,
		       COALESCE(sum(length(content)),0)::bigint
		FROM %s WHERE embedding IS NULL`, wikiStagingTable),
	).Scan(&r.Chunks, &r.Pages, &r.Chars)
	if err != nil {
		return domain.WikiRemaining{}, fmt.Errorf("postgres: staging remaining: %w", err)
	}
	return r, nil
}

func (s *Store) UnembeddedStaging(ctx context.Context, limit int) ([]domain.WikiChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: unembedded staging: limit %d out of range", limit)
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT page_id, chunk_index, title, url, revision_id, corpus, content
		FROM %s WHERE embedding IS NULL
		ORDER BY page_id, chunk_index LIMIT $1`, wikiStagingTable), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: unembedded staging: %w", err)
	}
	defer rows.Close()
	out := []domain.WikiChunk{}
	for rows.Next() {
		var c domain.WikiChunk
		var idx int32
		if err := rows.Scan(&c.PageID, &idx, &c.Title, &c.URL, &c.RevisionID, &c.Corpus, &c.Content); err != nil {
			return nil, fmt.Errorf("postgres: scan staging chunk: %w", err)
		}
		c.ChunkIndex = int(idx)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateStagingEmbeddings(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	for _, c := range chunks {
		if len(c.Embedding) != domain.EmbeddingDim {
			return fmt.Errorf("postgres: update staging page %d: embedding has %d dims, want %d", c.PageID, len(c.Embedding), domain.EmbeddingDim)
		}
		if err := w.Write([]string{
			strconv.FormatInt(c.PageID, 10), strconv.Itoa(c.ChunkIndex), formatHalfVec(c.Embedding),
		}); err != nil {
			return fmt.Errorf("postgres: encode staging update row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("postgres: encode staging update rows: %w", err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE staging_embed_update
			(page_id bigint, chunk_index integer, embedding halfvec(1024)) ON COMMIT DROP`); err != nil {
			return fmt.Errorf("create temp: %w", err)
		}
		if _, err := tx.Conn().PgConn().CopyFrom(ctx, &buf,
			"COPY staging_embed_update (page_id, chunk_index, embedding) FROM STDIN WITH (FORMAT csv)"); err != nil {
			return fmt.Errorf("copy temp: %w", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s s SET embedding = u.embedding
			FROM staging_embed_update u
			WHERE s.page_id = u.page_id AND s.chunk_index = u.chunk_index`, wikiStagingTable)); err != nil {
			return fmt.Errorf("apply update: %w", err)
		}
		return nil
	})
}
```

`bytes`, `encoding/csv`, `strconv`, `formatHalfVec` already exist in this file.

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** `feat(wiki): embed staging chunks in place`.

---

## Task 5: New swap guard + checkpoint-on-swap (`FinalizeStaging`)

**Files:** `wiki_embed.go`, `wiki_embed_test.go`.

- [ ] **Step 1: Failing tests** `TestFinalizeStagingSwapAndHeal` (live orphan page9; build+embed staging for pages 1,2; finalize at "v2"; live becomes 2 rows, orphan gone, `StagingPlan("v2")` -> `PlanAlreadyCurrent`) and `TestFinalizeStagingRefusesNull` (staging with a NULL row -> error containing "unembedded").
- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Implement.** Rewrite `validateStagingTx` (drop the `staging == live` comparison) and change `FinalizeStaging`/`swapStaging` to checkpoint in-tx:

```go
func (s *Store) validateStagingTx(ctx context.Context, tx pgx.Tx) error {
	var staging, nulls int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+wikiStagingTable).Scan(&staging); err != nil {
		return fmt.Errorf("count staging chunks: %w", err)
	}
	if staging == 0 {
		return errors.New("staging corpus is empty, refusing to swap")
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+wikiStagingTable+" WHERE embedding IS NULL").Scan(&nulls); err != nil {
		return fmt.Errorf("count staging null embeddings: %w", err)
	}
	if nulls != 0 {
		return fmt.Errorf("%d staged chunks are unembedded", nulls)
	}
	return nil
}

func (s *Store) FinalizeStaging(ctx context.Context, corpus, version string, lastChangeTS time.Time, maintenanceWorkMem string, maxParallelWorkers int) error {
	if err := s.buildStagingIndex(ctx, maintenanceWorkMem, maxParallelWorkers); err != nil {
		return err
	}
	return s.swapStaging(ctx, corpus, version, lastChangeTS)
}
```

In `swapStaging` keep the rename `stmts` unchanged; replace the `MarkWikiCorpusEmbedded` call with:

```go
	var ts pgtype.Timestamptz
	if !lastChangeTS.IsZero() {
		ts = pgtype.Timestamptz{Time: lastChangeTS, Valid: true}
	}
	v := version
	// inside the BeginFunc, after the rename stmts:
	if err := s.queries.WithTx(tx).UpsertWikiSyncState(ctx, db.UpsertWikiSyncStateParams{
		Corpus: corpus, LastChangeTS: ts, DumpVersion: &v,
	}); err != nil {
		return fmt.Errorf("checkpoint corpus: %w", err)
	}
```

Update `swapStaging` signature to `(ctx, corpus, version string, lastChangeTS time.Time)`. Add `time` and `pgtype` imports. Confirm `UpsertWikiSyncStateParams.DumpVersion` is `*string` and `LastChangeTS` is `pgtype.Timestamptz` (match generated types; adjust if different).

- [ ] **Step 4: Run, expect PASS.**
- [ ] **Step 5: Commit** `feat(wiki): swap guard drops staging==live; checkpoint on swap`.

---

## Task 6: Rewire `internal/wiki` ingest + embed orchestration

**Files:** `internal/wiki/sync.go`, `internal/wiki/embedrun.go`; tests `internal/wiki/embedrun_test.go` (+ `sync_test.go` if present).

- [ ] **Step 1: Failing unit tests** with a fake `BulkStore` recording calls: `PlanAlreadyCurrent` -> no ingest/finalize; `PlanResumeEmbed` -> embed remaining + finalize, no reset/upsert/carry; `PlanBuild` (driven by `RunBulk` + `RunBulkEmbed`) -> reset, upsert, carry, ready, embed, finalize. Model on the existing fake if present.
- [ ] **Step 2: Run, expect FAIL.**
- [ ] **Step 3: Implement.** Replace `EmbedSource`/`EmbedSink`/`EmbedStore` with one `BulkStore`:

```go
type BulkStore interface {
	StagingPlan(ctx context.Context, version string) (BulkPlan, error)
	ResetStaging(ctx context.Context, version string) error
	UpsertStagingChunks(ctx context.Context, chunks []domain.WikiChunk) error
	CarryForwardEmbeddings(ctx context.Context) (int64, error)
	MarkStagingReady(ctx context.Context, version string) error
	StagingRemaining(ctx context.Context) (domain.WikiRemaining, error)
	UnembeddedStaging(ctx context.Context, limit int) ([]domain.WikiChunk, error)
	UpdateStagingEmbeddings(ctx context.Context, chunks []domain.WikiChunk) error
	FinalizeStaging(ctx context.Context, corpus, version string, lastChangeTS time.Time, mem string, workers int) error
}
```

Rewrite `RunBulkEmbed` to: read `StagingRemaining` (pending total + log), loop `UnembeddedStaging(superBatch)` -> `embedChunks` -> `UpdateStagingEmbeddings` until empty, then `FinalizeStaging(cfg.Corpus, cfg.DumpVersion, parseDumpTime(cfg.DumpVersion), cfg.MaintenanceWorkMem, cfg.MaxParallelWorkers)`. Drop the watermark/`created`/discard logic. `EstimateBulkEmbed` reads `StagingRemaining` instead of the watermark.

In `sync.go`, change the `Store` interface to `{ EnsureCorpus, ResetStaging, UpsertStagingChunks, CarryForwardEmbeddings, MarkStagingReady }`. `RunBulk(ctx, store, files, corpus)` flow: `EnsureCorpus`; `ResetStaging(files.Version)`; parse streams + `UpsertStagingChunks` per stream (no `TrimPages`); guard 0 pages; `CarryForwardEmbeddings` (log carried count); `MarkStagingReady(files.Version)`; return stats. `storePages` drops the `trims` slice and `TrimPages` call. Remove `SetSyncState` usage (checkpoint now at swap).

- [ ] **Step 4: Run unit tests, expect PASS:** `cd stack/backend && ~/sdk/go1.26.4/bin/go test ./internal/wiki/`.
- [ ] **Step 5: Commit** `refactor(wiki): orchestrate ingest+embed over staging`.

---

## Task 7: Wire `cmd/wikisync`, delete dead code, fix callers

**Files:** `cmd/wikisync/main.go`; `internal/store/postgres/wiki_embed.go`; affected `*_test.go`.

- [ ] **Step 1:** In `runBulk` (main.go) branch on the plan after `dl.Fetch`:

```go
plan, err := store.StagingPlan(ctx, files.Version)
if err != nil {
	return err
}
if plan == wiki.PlanAlreadyCurrent {
	logger.InfoContext(ctx, "corpus already current; nothing to do",
		slog.String("corpus", wikiCfg.Corpus), slog.String("version", files.Version))
	return nil
}
if plan == wiki.PlanBuild {
	ingestStats, err := wiki.RunBulk(ctx, store, files, wikiCfg.Corpus)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "ingest complete", /* existing fields */)
}
if dryRun {
	est, err := wiki.EstimateBulkEmbed(ctx, store)
	// ... existing dry-run log, return nil
}
embedStats, err := wiki.RunBulkEmbed(ctx, logger, store, newEmbedder(logger, embProvider, embedCfg), wiki.Config{ /* existing */ })
```

`RunBulk` already receives `files`; no signature change. For `PlanResumeEmbed` the `RunBulk` block is skipped and the run resumes straight into embed.

- [ ] **Step 2:** Grep and resolve every reference to removed methods, then delete the dead ones:

```bash
cd stack/backend && grep -rn "EmbedWatermark\|UnembeddedChunks\|EstimateRemaining\b\|CopyStagingChunks\|DiscardStagingIfStale\|CreateStaging\|MarkWikiCorpusEmbedded\|EmbedInProgress\|UnembeddedWikiChunks\|EstimateRemainingWikiChunks" internal cmd
```
Delete `EmbedWatermark`, `UnembeddedChunks`, `EstimateRemaining`, `CopyStagingChunks`, `DiscardStagingIfStale`, `CreateStaging` from `wiki_embed.go`. Keep `EmbedInProgress`/`stagingExists` if delta uses them (grep shows). Remove `UnembeddedWikiChunks` / `EstimateRemainingWikiChunks` / `MarkWikiCorpusEmbedded` from `queries/wiki.sql` only if unused; if any `.sql` changed, regenerate: `~/sdk/go1.26.4/bin/go run github.com/sqlc-dev/sqlc/cmd/sqlc generate` (or the repo's documented sqlc invocation).

- [ ] **Step 3:** Rewrite/delete old integration tests in `wiki_embed_test.go` that drove the removed API; keep `vecEmbedder`, `seedChunks`, `unitVec`, `withEmbedding`, `chunk`.

- [ ] **Step 4: Full backend gate.**

```bash
cd stack/backend && ~/sdk/go1.26.4/bin/go build ./... \
 && ~/sdk/go1.26.4/bin/go vet ./... \
 && TEST_DATABASE_URL='postgres://postgres:dev@localhost:5432/truthinstream?sslmode=disable' ~/sdk/go1.26.4/bin/go test -race ./... 2>&1 | tail -30
gofumpt -l -w . && golangci-lint run ./... 2>&1 | tail
```

- [ ] **Step 5: Commit** `feat(wiki): drive bulk populate from the staging plan`.

---

## Task 8: End-to-end verification against the real dump

- [ ] **Step 1:** `cd /home/clement/Documents/dev/projects/truth-in-stream && make wiki-populate 2>&1 | tail -30`. Expected: ingest into staging, carry-forward logged, embed only the diff, `bulk embed finalized; wiki_chunks now serves the embedded corpus`, exit 0 - the orphan that caused `staging has 279343, live has 279350` is healed.
- [ ] **Step 2:** Re-run: `make wiki-populate 2>&1 | tail -5`. Expected: `corpus already current; nothing to do`, exit 0, no embedding calls.
- [ ] **Step 3:** Commit any doc/comment fixes.

---

## Self-review notes

- **Spec coverage:** ingest-into-staging (T2,T6), carry-forward (T3), embed-in-place (T4), guard drop + checkpoint-on-swap (T5), state machine incl. no-op (T1,T7), dry-run leaves resume target (T7), delta untouched (T7 grep gate), orphan-heal test (T5), resume crash-safety via `building`/`ready` (T1). All covered.
- **Placeholder scan:** none; every code step shows code. T2 ON CONFLICT decision resolved to plain INSERT.
- **Type consistency:** `BulkPlan` defined in `wiki` (T1), consumed in `postgres` (T1) and `cmd` (T7); `FinalizeStaging(corpus, version, lastChangeTS, mem, workers)` identical in T5 and the T6 interface; `WikiRemaining{Chunks,Pages,Chars}` matches the existing `domain` type.
- **Risk:** `UpsertStagingChunks` runs from concurrent stream goroutines; plain INSERT into a fresh empty table with globally-unique page ids is conflict-free. The carry-forward UPDATE and per-batch temp updates are bounded statements, fine on local PG.
