# Wikipedia Category-Crawl Ingestion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a second, additive ingestion path: a DB-free category crawler publishes self-contained chunk jobs to a new RabbitMQ queue, and a dedicated worker embeds them and upserts them straight into live `wiki_chunks` — leaving the dump/delta pipeline and the `embedworker` fleet untouched.

**Architecture:** Producer (`cmd/wikicrawl`) walks Wikipedia categories via the MediaWiki Action API (`list=categorymembers`), fetches lead + body extracts, chunks them with the existing `wiki.Chunk`, and publishes one self-contained `CrawlJob` per chunk to `crawl.chunks.v1`. Consumer (`cmd/crawlworker`) drains that queue, embeds each chunk via Voyage, and writes content+vector atomically with a new `UpsertEmbeddedChunk` store method. Consumer logic lives in a new transport-free package `internal/crawljob` mirroring `internal/embedjob`.

**Tech Stack:** Go (stdlib `net/http`, `encoding/json`), `rabbitmq/amqp091-go` via `internal/queue`, Voyage via `internal/embed`, Postgres + pgvector via `internal/store/postgres`, sqlc (`sqlc/sqlc:1.31.1`). No new third-party dependency.

**Spec:** `docs/superpowers/specs/2026-06-15-wikipedia-category-crawl-ingestion-design.md`

**Working directory for all Go commands:** `stack/backend` (run `cd stack/backend` first; Go toolchain is `~/sdk/go1.26.4` — prepend its `bin` to `PATH` if `go` is 1.20).

**Conventions to follow:** table-driven tests, wrap errors with `%w`, `gofumpt`/`go vet`/`golangci-lint` clean, `go test -race ./...` green. Integration tests (build tag `integration`, needing a DB) DROP tables — only ever point them at a throwaway database, never the seeded dev DB.

---

## File map

| File | Create/Modify | Responsibility |
|---|---|---|
| `internal/config/config.go` | Modify | Add `LoadCrawl`, `LoadCrawlQueue`, `LoadCrawlWorker` (+ small refactors `loadQueue`, `loadEmbedWorker`). |
| `internal/config/config_test.go` | Modify | Tests for the three new loaders. |
| `queries/wiki.sql` | Modify | Add `UpsertEmbeddedChunk`. |
| `internal/store/postgres/db/*` | Generated | `sqlc generate` output (do not hand-edit). |
| `internal/store/postgres/wiki.go` | Modify | `(*Store).UpsertEmbeddedChunk`. |
| `internal/store/postgres/wiki_test.go` | Modify | Integration test for `UpsertEmbeddedChunk`. |
| `internal/crawljob/crawljob.go` | Create | `CrawlJob` message + `Worker` consumer logic. |
| `internal/crawljob/crawljob_test.go` | Create | Unit tests for `validate` + `Process` + retry. |
| `internal/wiki/mediawiki.go` | Modify | `FullExtracts` (body text); refactor `extractsBatch` to share with leads. |
| `internal/wiki/mediawiki_test.go` | Modify | `FullExtracts` test. |
| `internal/wiki/crawl.go` | Create | `CategoryMembers` BFS over the Action API. |
| `internal/wiki/crawl_test.go` | Create | `CategoryMembers` test (httptest). |
| `internal/wiki/crawlproduce.go` | Create | `RunCrawl`: pages → `CrawlJob`s → publish. |
| `internal/wiki/crawlproduce_test.go` | Create | `RunCrawl` test (fake source + publisher). |
| `cmd/wikicrawl/main.go`, `adapter.go` | Create | Producer binary + `qPublisher` adapter. |
| `cmd/crawlworker/main.go`, `adapter.go` | Create | Consumer binary + `qStream`/`qEnqueuer`/`qDelivery` adapters. |
| `cmd/crawlworker/integration_test.go` | Create | Broker+DB round-trip (build tag `integration`). |
| `docker-compose.yml` | Modify | `wikicrawl` + `crawlworker` services under the `wiki` profile. |
| `stack/backend/Makefile` + root `Makefile` | Modify | `crawl`, `crawl-workers` targets. |
| `docs/ingestion-pipeline.md` | Modify | Document the category-crawl path. |

Build bottom-up: config → store → crawljob → wiki crawler/produce → binaries → compose/make → docs. Each task ends green and committed.

---

## Task 1: Config loaders

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go`:

```go
func TestLoadCrawl(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics, Category:Chemistry")
	t.Setenv("CRAWL_PROJECT", "simplewiki")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if got, want := len(c.Categories), 2; got != want {
		t.Fatalf("categories = %d, want %d", got, want)
	}
	if c.Categories[0] != "Category:Physics" || c.Categories[1] != "Category:Chemistry" {
		t.Errorf("categories = %v, want trimmed pair", c.Categories)
	}
	if c.Corpus != "simplewiki-crawl" {
		t.Errorf("corpus = %q, want simplewiki-crawl", c.Corpus)
	}
	if c.Project != "simplewiki" {
		t.Errorf("project = %q, want simplewiki", c.Project)
	}
	if c.MaxDepth != 1 || c.MaxPages != 5000 || !c.IncludeBody {
		t.Errorf("defaults wrong: depth=%d pages=%d body=%v", c.MaxDepth, c.MaxPages, c.IncludeBody)
	}
}

func TestLoadCrawlRequiresCategories(t *testing.T) {
	if _, err := LoadCrawl(); err == nil {
		t.Fatal("LoadCrawl with no CRAWL_CATEGORIES = nil error, want error")
	}
}

func TestLoadCrawlIncludeBodyFalse(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_INCLUDE_BODY", "false")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if c.IncludeBody {
		t.Error("IncludeBody = true, want false")
	}
}

func TestLoadCrawlQueueDefaultName(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://localhost")
	q, err := LoadCrawlQueue()
	if err != nil {
		t.Fatalf("LoadCrawlQueue: %v", err)
	}
	if q.VersionedName() != "crawl.chunks.v1" {
		t.Errorf("VersionedName = %q, want crawl.chunks.v1", q.VersionedName())
	}
}

func TestLoadCrawlWorkerDefaults(t *testing.T) {
	w, err := LoadCrawlWorker()
	if err != nil {
		t.Fatalf("LoadCrawlWorker: %v", err)
	}
	if w.Concurrency != 4 || w.MaxAttempts != 5 {
		t.Errorf("defaults wrong: concurrency=%d attempts=%d", w.Concurrency, w.MaxAttempts)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd stack/backend && go test ./internal/config/ -run 'Crawl' -v`
Expected: FAIL — `LoadCrawl`, `LoadCrawlQueue`, `LoadCrawlWorker` undefined.

- [ ] **Step 3: Refactor `loadQueue` and `loadEmbedWorker`, add crawl loaders**

In `internal/config/config.go`, change `LoadQueue` to delegate to a private helper and add `LoadCrawlQueue`:

```go
// defaultCrawlQueueName is the base queue the category crawler publishes to,
// kept separate from the embedding-jobs queue so the crawl worker and the
// dump-pipeline fleet never consume each other's messages.
const defaultCrawlQueueName = "crawl.chunks"

// LoadQueue reads the embedding-jobs broker configuration from the environment.
func LoadQueue() (Queue, error) {
	return loadQueue("RABBITMQ_QUEUE", defaultQueueName)
}

// LoadCrawlQueue reads the category-crawl broker configuration. It shares the
// broker URL, priority, prefetch, and version machinery with LoadQueue but binds
// to its own base queue name (RABBITMQ_CRAWL_QUEUE, default crawl.chunks).
func LoadCrawlQueue() (Queue, error) {
	return loadQueue("RABBITMQ_CRAWL_QUEUE", defaultCrawlQueueName)
}

func loadQueue(nameEnv, nameDefault string) (Queue, error) {
	url, err := requireEnv("RABBITMQ_URL")
	if err != nil {
		return Queue{}, err
	}
	maxPriority, err := intEnv("RABBITMQ_MAX_PRIORITY", defaultQueueMaxPriority, 1, math.MaxUint8)
	if err != nil {
		return Queue{}, err
	}
	prefetch, err := intEnv("RABBITMQ_PREFETCH", defaultQueuePrefetch, 0, math.MaxUint16)
	if err != nil {
		return Queue{}, err
	}
	versions, err := queueVersions(getenv("RABBITMQ_QUEUE_VERSIONS", defaultQueueVersion))
	if err != nil {
		return Queue{}, err
	}
	return Queue{
		URL:           url,
		Name:          getenv(nameEnv, nameDefault),
		MaxPriority:   uint8(maxPriority),
		Prefetch:      prefetch,
		Version:       versions[len(versions)-1],
		KnownVersions: versions,
	}, nil
}
```

Change `LoadEmbedWorker` to delegate, and add `LoadCrawlWorker`:

```go
// LoadEmbedWorker reads the embedding-worker configuration (EMBED_WORKER_*).
func LoadEmbedWorker() (EmbedWorker, error) {
	return loadEmbedWorker("EMBED_WORKER")
}

// LoadCrawlWorker reads the crawl-worker configuration (CRAWL_WORKER_*). It
// reuses the EmbedWorker shape and defaults; only the env prefix differs.
func LoadCrawlWorker() (EmbedWorker, error) {
	return loadEmbedWorker("CRAWL_WORKER")
}

func loadEmbedWorker(prefix string) (EmbedWorker, error) {
	w := EmbedWorker{
		Concurrency:       defaultEmbedWorkerConcurrency,
		MaxAttempts:       defaultEmbedWorkerMaxAttempts,
		HTTPTimeout:       defaultEmbedWorkerHTTPTimeout,
		RequestsPerMinute: defaultEmbedWorkerRequestsPerMinute,
		EmbedMaxRetries:   defaultEmbedWorkerEmbedMaxRetries,
	}
	var err error
	if w.Concurrency, err = intEnv(prefix+"_CONCURRENCY", w.Concurrency, 1, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	if w.MaxAttempts, err = intEnv(prefix+"_MAX_ATTEMPTS", w.MaxAttempts, 1, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	if w.HTTPTimeout, err = positiveDurationEnv(prefix+"_HTTP_TIMEOUT", w.HTTPTimeout); err != nil {
		return EmbedWorker{}, err
	}
	if w.RequestsPerMinute, err = intEnv(prefix+"_RPM", w.RequestsPerMinute, 0, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	if w.EmbedMaxRetries, err = intEnv(prefix+"_EMBED_MAX_RETRIES", w.EmbedMaxRetries, 1, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	return w, nil
}
```

Add the `Crawl` config type, defaults, and `LoadCrawl` (place near the wiki config block):

```go
// Crawl-ingestion defaults: crawl one subcategory level deep, cap at a few
// thousand pages so an unattended run is bounded, and ingest body prose by
// default since lead-only is the explicit opt-out.
const (
	defaultCrawlMaxDepth = 1
	defaultCrawlMaxPages = 5000
)

// Crawl configures the category crawler. Categories are the seed category titles
// (e.g. "Category:Physics"); Project is the wiki project whose Action API is
// queried and whose host builds article URLs (defaults to WIKI_CORPUS); Corpus is
// the provenance tag written to wiki_chunks.corpus (defaults to "<project>-crawl"
// so crawl rows are isolated from the dump corpus's delta checkpoint); MaxDepth
// bounds subcategory recursion (0 = direct pages only); MaxPages caps distinct
// pages collected; IncludeBody adds kind='body' chunks alongside the lead.
type Crawl struct {
	Categories  []string
	Project     string
	Corpus      string
	MaxDepth    int
	MaxPages    int
	IncludeBody bool
}

// LoadCrawl reads the category-crawl configuration. CRAWL_CATEGORIES is required
// (a comma-separated list of category titles); the rest default. Bad values fail
// fast at startup.
func LoadCrawl() (Crawl, error) {
	raw, err := requireEnv("CRAWL_CATEGORIES")
	if err != nil {
		return Crawl{}, err
	}
	categories := make([]string, 0)
	for _, p := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(p); v != "" {
			categories = append(categories, v)
		}
	}
	if len(categories) == 0 {
		return Crawl{}, fmt.Errorf("config: CRAWL_CATEGORIES %q has no category", raw)
	}

	project := getenv("CRAWL_PROJECT", getenv("WIKI_CORPUS", defaultWikiCorpus))
	corpus := getenv("CRAWL_CORPUS", project+"-crawl")

	maxDepth, err := intEnv("CRAWL_MAX_DEPTH", defaultCrawlMaxDepth, 0, math.MaxInt32)
	if err != nil {
		return Crawl{}, err
	}
	maxPages, err := intEnv("CRAWL_MAX_PAGES", defaultCrawlMaxPages, 1, math.MaxInt32)
	if err != nil {
		return Crawl{}, err
	}

	includeBody := true
	if v := os.Getenv("CRAWL_INCLUDE_BODY"); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return Crawl{}, fmt.Errorf("config: CRAWL_INCLUDE_BODY %q: %w", v, perr)
		}
		includeBody = b
	}

	return Crawl{
		Categories:  categories,
		Project:     project,
		Corpus:      corpus,
		MaxDepth:    maxDepth,
		MaxPages:    maxPages,
		IncludeBody: includeBody,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd stack/backend && go test ./internal/config/ -run 'Crawl|Queue|EmbedWorker' -v`
Expected: PASS (new tests + the existing queue/worker tests still green after the refactor).

- [ ] **Step 5: Commit**

```bash
cd stack/backend && gofumpt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): crawl, crawl-queue, and crawl-worker loaders"
```

---

## Task 2: `UpsertEmbeddedChunk` store method

**Files:**
- Modify: `queries/wiki.sql`
- Modify: `internal/store/postgres/wiki.go`
- Test: `internal/store/postgres/wiki_test.go`

- [ ] **Step 1: Add the SQL query**

Append to `queries/wiki.sql`:

```sql
-- name: UpsertEmbeddedChunk :exec
-- Crawl ingestion writes content and embedding together: the worker embeds the
-- self-contained message, then upserts the whole row in one statement so a chunk
-- is never visible to search without its matching vector. The embedding is always
-- the freshly computed one, so a re-crawl rewrites the same vector idempotently.
INSERT INTO wiki_chunks (page_id, chunk_index, title, url, revision_id, corpus, content, section, kind, embedding, synced_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
ON CONFLICT (page_id, chunk_index) DO UPDATE
    SET title = EXCLUDED.title,
        url = EXCLUDED.url,
        revision_id = EXCLUDED.revision_id,
        corpus = EXCLUDED.corpus,
        content = EXCLUDED.content,
        section = EXCLUDED.section,
        kind = EXCLUDED.kind,
        embedding = EXCLUDED.embedding,
        synced_at = now();
```

- [ ] **Step 2: Regenerate sqlc code**

Run: `cd stack/backend && make sqlc`
Expected: `internal/store/postgres/db/` regenerates with `UpsertEmbeddedChunk` + `UpsertEmbeddedChunkParams` (Embedding `*pgvector.HalfVector`, plus the nine column fields). Verify: `git status` shows changes under `internal/store/postgres/db/`.

- [ ] **Step 3: Write the failing integration test**

Add to `internal/store/postgres/wiki_test.go` (this file already carries `//go:build integration` and a `newTestStore`/`resetWiki` style helper — follow the existing helpers in the file):

```go
func TestUpsertEmbeddedChunkRoundTrip(t *testing.T) {
	store, ctx := newTestStore(t)
	emb := make([]float32, domain.EmbeddingDim)
	for i := range emb {
		emb[i] = 0.01
	}
	chunk := domain.WikiChunk{
		PageID: 7, ChunkIndex: 0, Title: "Atom", URL: "https://simple.wikipedia.org/wiki/Atom",
		RevisionID: 11, Corpus: "simplewiki-crawl", Content: "Atom\n\nAn atom is matter.",
		Section: "", Kind: domain.WikiChunkKindBody, Embedding: emb,
	}
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		t.Fatalf("UpsertEmbeddedChunk: %v", err)
	}
	got, err := store.GetWikiChunk(ctx, 7, 0)
	if err != nil {
		t.Fatalf("GetWikiChunk: %v", err)
	}
	if got.Content != chunk.Content || got.Kind != domain.WikiChunkKindBody || got.Corpus != "simplewiki-crawl" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if len(got.Embedding) != domain.EmbeddingDim {
		t.Errorf("embedding dims = %d, want %d", len(got.Embedding), domain.EmbeddingDim)
	}
}

func TestUpsertEmbeddedChunkRejectsWrongDim(t *testing.T) {
	store, ctx := newTestStore(t)
	chunk := domain.WikiChunk{PageID: 1, ChunkIndex: 0, Corpus: "c", Content: "x",
		Kind: domain.WikiChunkKindLead, Embedding: make([]float32, 3)}
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err == nil {
		t.Fatal("UpsertEmbeddedChunk with 3 dims = nil error, want error")
	}
}

func TestUpsertEmbeddedChunkRejectsInvalidKind(t *testing.T) {
	store, ctx := newTestStore(t)
	emb := make([]float32, domain.EmbeddingDim)
	chunk := domain.WikiChunk{PageID: 1, ChunkIndex: 0, Corpus: "c", Content: "x",
		Kind: domain.WikiChunkKind("sidebar"), Embedding: emb}
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err == nil {
		t.Fatal("UpsertEmbeddedChunk with invalid kind = nil error, want error")
	}
}
```

> If the helper names differ (`newTestStore`, `GetWikiChunk` signature), match what `wiki_test.go` / `wiki_delta_test.go` already use — `GetWikiChunk` exists per `queries/wiki.sql`; reuse its store wrapper if present, else read it directly in the test.

- [ ] **Step 4: Run to verify it fails**

Run: `cd stack/backend && go test -tags integration ./internal/store/postgres/ -run UpsertEmbeddedChunk -v`
(needs `TEST_DATABASE_URL` pointed at a **throwaway** DB.)
Expected: FAIL — `store.UpsertEmbeddedChunk` undefined.

- [ ] **Step 5: Implement the store method**

Add to `internal/store/postgres/wiki.go` (next to `SetChunkEmbeddings`):

```go
// UpsertEmbeddedChunk inserts or replaces one wiki chunk together with its
// embedding in a single statement, so the row is never visible to search without
// a matching vector. The crawl worker calls it after embedding a self-contained
// message; the embedding is written as text-form ::halfvec by the generated
// query (never binary COPY). The embedding must be full-dimension and the kind
// valid.
func (s *Store) UpsertEmbeddedChunk(ctx context.Context, c domain.WikiChunk) error {
	if !c.Kind.Valid() {
		return fmt.Errorf("postgres: upsert embedded chunk page %d: invalid kind %q", c.PageID, c.Kind)
	}
	if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
		return fmt.Errorf("postgres: upsert embedded chunk page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
	}
	if len(c.Embedding) != domain.EmbeddingDim {
		return fmt.Errorf("postgres: upsert embedded chunk page %d chunk %d: embedding has %d dims, want %d", c.PageID, c.ChunkIndex, len(c.Embedding), domain.EmbeddingDim)
	}
	hv := pgvector.NewHalfVector(c.Embedding)
	if err := s.queries.UpsertEmbeddedChunk(ctx, db.UpsertEmbeddedChunkParams{
		PageID:     c.PageID,
		ChunkIndex: int32(c.ChunkIndex),
		Title:      c.Title,
		Url:        c.URL,
		RevisionID: c.RevisionID,
		Corpus:     c.Corpus,
		Content:    c.Content,
		Section:    c.Section,
		Kind:       string(c.Kind),
		Embedding:  &hv,
	}); err != nil {
		return fmt.Errorf("postgres: upsert embedded chunk page %d chunk %d: %w", c.PageID, c.ChunkIndex, err)
	}
	return nil
}
```

- [ ] **Step 6: Run to verify it passes**

Run: `cd stack/backend && go test -tags integration ./internal/store/postgres/ -run UpsertEmbeddedChunk -v`
Expected: PASS. Also run `make sqlc-verify` → no diff.

- [ ] **Step 7: Commit**

```bash
cd stack/backend && gofumpt -w internal/store/postgres/wiki.go internal/store/postgres/wiki_test.go
git add queries/wiki.sql internal/store/postgres/
git commit -m "feat(store): UpsertEmbeddedChunk writes content and vector atomically"
```

---

## Task 3: `internal/crawljob` consumer package

**Files:**
- Create: `internal/crawljob/crawljob.go`
- Test: `internal/crawljob/crawljob_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/crawljob/crawljob_test.go`:

```go
package crawljob

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeEmbedder struct {
	vec [][]float32
	err error
}

func (f fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

type fakeStore struct {
	got domain.WikiChunk
	err error
}

func (f *fakeStore) UpsertEmbeddedChunk(_ context.Context, c domain.WikiChunk) error {
	f.got = c
	return f.err
}

func fullVec() []float32 { return make([]float32, domain.EmbeddingDim) }

func validJob() CrawlJob {
	return CrawlJob{PageID: 5, ChunkIndex: 1, Title: "Atom", URL: "u", RevisionID: 9,
		Corpus: "simplewiki-crawl", Content: "Atom\n\ntext", Section: "", Kind: "body"}
}

func mustBody(t *testing.T, j CrawlJob) []byte {
	t.Helper()
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestProcessHappyPathUpserts(t *testing.T) {
	st := &fakeStore{}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
	res := w.Process(context.Background(), mustBody(t, validJob()), 5)
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want Ack", res.Action)
	}
	if st.got.PageID != 5 || st.got.Kind != domain.WikiChunkKindBody || len(st.got.Embedding) != domain.EmbeddingDim {
		t.Errorf("upserted chunk wrong: %+v", st.got)
	}
}

func TestProcessMalformedIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	if res := w.Process(context.Background(), []byte("{not json"), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessInvalidJobIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	bad := validJob()
	bad.Content = ""
	if res := w.Process(context.Background(), mustBody(t, bad), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessWrongDimIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{vec: [][]float32{{0.1, 0.2}}}, &fakeStore{}, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(context.Background(), mustBody(t, validJob()), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessTransientFailureRepublishes(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 3})
	res := w.Process(context.Background(), mustBody(t, validJob()), 7)
	if res.Action != ActionRepublish || res.RepublishPriority != 7 {
		t.Fatalf("action=%v prio=%d, want Republish @7", res.Action, res.RepublishPriority)
	}
	var retried CrawlJob
	if err := json.Unmarshal(res.RepublishBody, &retried); err != nil {
		t.Fatalf("unmarshal retry: %v", err)
	}
	if retried.Attempt != 1 {
		t.Errorf("retry attempt = %d, want 1", retried.Attempt)
	}
}

func TestProcessExhaustedAttemptsDropped(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 2})
	j := validJob()
	j.Attempt = 1 // already at budget-1
	if res := w.Process(context.Background(), mustBody(t, j), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop after retries)", res.Action)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*CrawlJob)
		ok   bool
	}{
		{"valid", func(*CrawlJob) {}, true},
		{"page id zero", func(j *CrawlJob) { j.PageID = 0 }, false},
		{"negative index", func(j *CrawlJob) { j.ChunkIndex = -1 }, false},
		{"empty content", func(j *CrawlJob) { j.Content = "" }, false},
		{"empty corpus", func(j *CrawlJob) { j.Corpus = "" }, false},
		{"bad kind", func(j *CrawlJob) { j.Kind = "sidebar" }, false},
		{"negative revision", func(j *CrawlJob) { j.RevisionID = -1 }, false},
		{"negative attempt", func(j *CrawlJob) { j.Attempt = -1 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := validJob()
			tc.mut(&j)
			if err := j.validate(); (err == nil) != tc.ok {
				t.Errorf("validate() err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd stack/backend && go test ./internal/crawljob/ -v`
Expected: FAIL — package/types undefined.

- [ ] **Step 3: Implement the package**

Create `internal/crawljob/crawljob.go`:

```go
// Package crawljob is the crawl-worker consumer logic: it drains self-contained
// chunk jobs from the crawl queue, embeds each chunk's text through the Voyage
// embedder, and upserts the chunk (content + vector) straight into the live wiki
// corpus. It is transport-free - it depends on its own small Stream/Delivery/
// Store/Enqueuer interfaces, never on a concrete broker or any HTTP type - so the
// worker is unit-testable and the broker is swappable behind the cmd-layer
// adapters. It mirrors internal/embedjob but writes whole chunks (the message is
// self-contained) instead of filling an embedding on a pre-staged row.
package crawljob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// CrawlJob is one unit of crawl-ingest work: a fully self-contained Wikipedia
// chunk. Every field needed to write a live wiki_chunks row travels in the body,
// so the worker performs no database read before writing. Attempt is the delivery
// attempt so far; the producer leaves it zero and the worker increments it on a
// transient-failure re-enqueue so a job that keeps failing is eventually dropped.
type CrawlJob struct {
	PageID     int64  `json:"page_id"`
	ChunkIndex int    `json:"chunk_index"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	RevisionID int64  `json:"revision_id"`
	Corpus     string `json:"corpus"`
	Content    string `json:"content"`
	Section    string `json:"section"`
	Kind       string `json:"kind"`
	Attempt    int    `json:"attempt,omitzero"`
}

// validate rejects a job that can never succeed, so the worker drops it instead
// of embedding nonsense or looping forever.
func (j CrawlJob) validate() error {
	switch {
	case j.PageID <= 0:
		return fmt.Errorf("page id %d must be positive", j.PageID)
	case j.ChunkIndex < 0:
		return fmt.Errorf("chunk index %d must not be negative", j.ChunkIndex)
	case j.Content == "":
		return fmt.Errorf("page %d chunk %d has empty content", j.PageID, j.ChunkIndex)
	case j.Corpus == "":
		return fmt.Errorf("page %d chunk %d has empty corpus", j.PageID, j.ChunkIndex)
	case !domain.WikiChunkKind(j.Kind).Valid():
		return fmt.Errorf("page %d chunk %d has invalid kind %q", j.PageID, j.ChunkIndex, j.Kind)
	case j.RevisionID < 0:
		return fmt.Errorf("page %d chunk %d has a negative revision %d", j.PageID, j.ChunkIndex, j.RevisionID)
	case j.Attempt < 0:
		return fmt.Errorf("page %d chunk %d has a negative attempt %d", j.PageID, j.ChunkIndex, j.Attempt)
	default:
		return nil
	}
}

// Action is what the consume loop must do with a delivery after Process decides
// the job's fate.
type Action int

const (
	// ActionAck drops the delivery: handled, obsolete, or unprocessable.
	ActionAck Action = iota
	// ActionRepublish re-enqueues the job (attempt incremented) then drops the original.
	ActionRepublish
	// ActionRequeue returns the delivery unhandled because shutdown cut work short.
	ActionRequeue
)

// Result is the outcome of processing one message.
type Result struct {
	Action            Action
	RepublishBody     []byte
	RepublishPriority uint8
}

// Embedder embeds chunk text for storage; the Voyage client with its retry and
// rate-limit decorators satisfies it.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// Store upserts a fully embedded chunk into the live corpus. The write is
// idempotent: a redelivered job rewrites the same row.
type Store interface {
	UpsertEmbeddedChunk(ctx context.Context, chunk domain.WikiChunk) error
}

// Delivery is one job message awaiting acknowledgement, abstracting the broker.
type Delivery interface {
	Body() []byte
	Priority() uint8
	Version() string
	Ack() error
	Nack(requeue bool) error
}

// Stream yields deliveries until ctx is canceled, then closes the channel.
type Stream interface {
	Consume(ctx context.Context) (<-chan Delivery, error)
}

// Enqueuer re-enqueues a job body at a priority for a bounded retry.
type Enqueuer interface {
	Enqueue(ctx context.Context, body []byte, priority uint8) error
}

// Config tunes a Worker. Concurrency caps parallel embeds per replica;
// MaxAttempts is the per-job delivery budget; KnownVersions is the set of queue
// schema versions this worker understands (empty disables the check).
type Config struct {
	Concurrency   int
	MaxAttempts   int
	KnownVersions []string
}

// Worker drains crawl jobs and upserts their embedded chunks into the live corpus.
type Worker struct {
	embedder      Embedder
	store         Store
	stream        Stream
	enqueuer      Enqueuer
	logger        *slog.Logger
	concurrency   int
	maxAttempts   int
	knownVersions map[string]struct{}
}

// NewWorker builds a Worker, clamping concurrency and attempts to at least one
// and defaulting a nil logger.
func NewWorker(embedder Embedder, store Store, stream Stream, enqueuer Enqueuer, logger *slog.Logger, cfg Config) *Worker {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	var known map[string]struct{}
	if len(cfg.KnownVersions) > 0 {
		known = make(map[string]struct{}, len(cfg.KnownVersions))
		for _, v := range cfg.KnownVersions {
			known[v] = struct{}{}
		}
	}
	return &Worker{
		embedder:      embedder,
		store:         store,
		stream:        stream,
		enqueuer:      enqueuer,
		logger:        logger,
		concurrency:   cfg.Concurrency,
		maxAttempts:   cfg.MaxAttempts,
		knownVersions: known,
	}
}

func (w *Worker) knowsVersion(version string) bool {
	if w.knownVersions == nil {
		return true
	}
	_, ok := w.knownVersions[version]
	return ok
}

// Run consumes the queue until ctx is canceled, processing up to Concurrency jobs
// in parallel. On shutdown an in-flight handler leaves its delivery unacked so the
// broker redelivers it; the idempotent upsert makes the re-embed safe.
func (w *Worker) Run(ctx context.Context) error {
	deliveries, err := w.stream.Consume(ctx)
	if err != nil {
		return fmt.Errorf("crawljob: start consumer: %w", err)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, w.concurrency)
loop:
	for d := range deliveries {
		select {
		case sem <- struct{}{}:
		default:
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break loop
			}
		}
		wg.Add(1)
		go func(d Delivery) {
			defer wg.Done()
			defer func() { <-sem }()
			w.handle(ctx, d)
		}(d)
	}
	wg.Wait()
	return nil
}

func (w *Worker) handle(ctx context.Context, d Delivery) {
	if !w.knowsVersion(d.Version()) {
		w.logger.ErrorContext(ctx, "dropping crawl job with unknown queue version", slog.String("version", d.Version()))
		w.ack(ctx, d)
		return
	}
	res := w.Process(ctx, d.Body(), d.Priority())
	switch res.Action {
	case ActionRepublish:
		if err := w.enqueuer.Enqueue(ctx, res.RepublishBody, res.RepublishPriority); err != nil {
			w.logger.ErrorContext(ctx, "re-enqueue failed, requeuing original delivery", slog.Any("err", err))
			w.nack(ctx, d, true)
			return
		}
		w.ack(ctx, d)
	case ActionRequeue:
		w.nack(ctx, d, true)
	default:
		w.ack(ctx, d)
	}
}

func (w *Worker) ack(ctx context.Context, d Delivery) {
	if err := d.Ack(); err != nil {
		w.logger.ErrorContext(ctx, "ack failed", slog.Any("err", err))
	}
}

func (w *Worker) nack(ctx context.Context, d Delivery, requeue bool) {
	if err := d.Nack(requeue); err != nil {
		w.logger.ErrorContext(ctx, "nack failed", slog.Any("err", err), slog.Bool("requeue", requeue))
	}
}

// Process embeds the job in body and upserts its chunk, returning the action the
// caller must take. It never returns an error: a malformed or invalid message and
// a persistent failure fold into ActionAck (after an ERROR log), a transient
// failure into ActionRepublish, and a shutdown into ActionRequeue.
func (w *Worker) Process(ctx context.Context, body []byte, priority uint8) Result {
	var job CrawlJob
	if err := json.Unmarshal(body, &job); err != nil {
		w.logger.ErrorContext(ctx, "dropping malformed crawl job", slog.Any("err", err))
		return Result{Action: ActionAck}
	}
	if err := job.validate(); err != nil {
		w.logger.ErrorContext(ctx, "dropping invalid crawl job", slog.Any("err", err))
		return Result{Action: ActionAck}
	}

	embeddings, err := w.embedder.EmbedDocuments(ctx, []string{job.Content})
	if err != nil {
		return w.afterFailure(ctx, job, priority, "embed", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != domain.EmbeddingDim {
		got := 0
		if len(embeddings) == 1 {
			got = len(embeddings[0])
		}
		w.logger.ErrorContext(ctx, "dropping crawl job with unexpected provider response",
			slog.Int64("page_id", job.PageID), slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("vectors", len(embeddings)), slog.Int("dims", got), slog.Int("want_dims", domain.EmbeddingDim))
		return Result{Action: ActionAck}
	}

	chunk := domain.WikiChunk{
		PageID: job.PageID, ChunkIndex: job.ChunkIndex, Title: job.Title, URL: job.URL,
		RevisionID: job.RevisionID, Corpus: job.Corpus, Content: job.Content, Section: job.Section,
		Kind: domain.WikiChunkKind(job.Kind), Embedding: embeddings[0],
	}
	if err := w.store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		return w.afterFailure(ctx, job, priority, "upsert", err)
	}
	return Result{Action: ActionAck}
}

// afterFailure decides what to do with a job whose embed or upsert failed. A
// canceled context means shutdown: requeue without counting the attempt.
// Otherwise the attempt counts: drop with an ERROR log when the budget is spent,
// else re-enqueue with the attempt incremented at the same priority.
func (w *Worker) afterFailure(ctx context.Context, job CrawlJob, priority uint8, stage string, cause error) Result {
	if ctx.Err() != nil {
		w.logger.InfoContext(ctx, "crawl job interrupted by shutdown, requeuing",
			slog.String("stage", stage), slog.Int64("page_id", job.PageID), slog.Int("chunk_index", job.ChunkIndex))
		return Result{Action: ActionRequeue}
	}
	if job.Attempt >= w.maxAttempts-1 {
		w.logger.ErrorContext(ctx, "dropping crawl job after exhausting retries",
			slog.String("stage", stage), slog.Int64("page_id", job.PageID), slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("attempt", job.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
		return Result{Action: ActionAck}
	}
	retry := job
	retry.Attempt = job.Attempt + 1
	encoded, err := json.Marshal(retry)
	if err != nil {
		w.logger.ErrorContext(ctx, "dropping crawl job that cannot be re-encoded for retry",
			slog.Int64("page_id", job.PageID), slog.Int("chunk_index", job.ChunkIndex), slog.Any("err", err))
		return Result{Action: ActionAck}
	}
	w.logger.WarnContext(ctx, "crawl job failed, re-enqueuing for retry",
		slog.String("stage", stage), slog.Int64("page_id", job.PageID), slog.Int("chunk_index", job.ChunkIndex),
		slog.Int("next_attempt", retry.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
	return Result{Action: ActionRepublish, RepublishBody: encoded, RepublishPriority: priority}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd stack/backend && go test -race ./internal/crawljob/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd stack/backend && gofumpt -w internal/crawljob/
git add internal/crawljob/
git commit -m "feat(crawljob): self-contained crawl-job consumer logic"
```

---

## Task 4: `FullExtracts` (body text) on the API client

**Files:**
- Modify: `internal/wiki/mediawiki.go`
- Test: `internal/wiki/mediawiki_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/wiki/mediawiki_test.go` (follow the existing httptest pattern in that file — a handler that inspects `r.URL.Query()` and writes a JSON fixture, with the client pointed at `srv.URL` via `BaseURL`):

```go
func TestFullExtractsReturnsBodyText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("exintro") != "" {
			t.Errorf("FullExtracts must not set exintro, got %q", q.Get("exintro"))
		}
		if q.Get("explaintext") != "1" {
			t.Errorf("explaintext = %q, want 1", q.Get("explaintext"))
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"10":{"pageid":10,"title":"Atom","extract":"Atom intro.\n\nBody section text.","revisions":[{"revid":99}]}}}}`))
	}))
	defer srv.Close()

	c := &APIClient{BaseURL: srv.URL}
	got, err := c.FullExtracts(context.Background(), []string{"Atom"})
	if err != nil {
		t.Fatalf("FullExtracts: %v", err)
	}
	if len(got) != 1 || got[0].PageID != 10 || got[0].RevisionID != 99 {
		t.Fatalf("got %+v, want pageid 10 rev 99", got)
	}
	if got[0].Text != "Atom intro.\n\nBody section text." {
		t.Errorf("text = %q", got[0].Text)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd stack/backend && go test ./internal/wiki/ -run FullExtracts -v`
Expected: FAIL — `FullExtracts` undefined.

- [ ] **Step 3: Refactor `extractsBatch` to share and add `FullExtracts`**

In `internal/wiki/mediawiki.go`, replace `Extracts` + `extractsBatch` with a shared core (keep the existing `Extracts` behavior identical — it still sets `exintro`):

```go
// Extracts fetches the plain-text lead section and current revision id of each
// title, in batches of at most the extracts limit. A title with no live page
// comes back flagged Missing.
func (c *APIClient) Extracts(ctx context.Context, titles []string) ([]Extract, error) {
	return c.extractsAll(ctx, titles, true)
}

// FullExtracts fetches the full plain-text article and current revision id of
// each title (no exintro), in batches of at most the extracts limit. It is the
// body-text source for crawl ingestion; the lead is stripped from the front
// downstream so a chunk is not embedded twice.
func (c *APIClient) FullExtracts(ctx context.Context, titles []string) ([]Extract, error) {
	return c.extractsAll(ctx, titles, false)
}

func (c *APIClient) extractsAll(ctx context.Context, titles []string, intro bool) ([]Extract, error) {
	out := make([]Extract, 0, len(titles))
	for start := 0; start < len(titles); start += extractsBatchMax {
		end := min(start+extractsBatchMax, len(titles))
		batch, err := c.extractsBatch(ctx, titles[start:end], intro)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (c *APIClient) extractsBatch(ctx context.Context, titles []string, intro bool) ([]Extract, error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("prop", "extracts|revisions")
	if intro {
		params.Set("exintro", "1")
	}
	params.Set("explaintext", "1")
	params.Set("exlimit", strconv.Itoa(extractsBatchMax))
	params.Set("rvprop", "ids")
	params.Set("titles", strings.Join(titles, "|"))

	var resp exResponse
	if err := c.getJSON(ctx, params, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("wiki: extracts api error %s: %s", resp.Error.Code, resp.Error.Info)
	}
	out := make([]Extract, 0, len(resp.Query.Pages))
	for _, p := range resp.Query.Pages {
		if p.Missing != nil {
			out = append(out, Extract{Title: p.Title, Missing: true})
			continue
		}
		ex := Extract{PageID: p.PageID, Title: p.Title, Text: p.Extract}
		if len(p.Revisions) > 0 {
			ex.RevisionID = p.Revisions[0].RevID
		}
		out = append(out, ex)
	}
	return out, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd stack/backend && go test ./internal/wiki/ -run 'Extracts' -v`
Expected: PASS (new `FullExtracts` test + existing `Extracts` tests).

- [ ] **Step 5: Commit**

```bash
cd stack/backend && gofumpt -w internal/wiki/mediawiki.go internal/wiki/mediawiki_test.go
git add internal/wiki/mediawiki.go internal/wiki/mediawiki_test.go
git commit -m "feat(wiki): FullExtracts for body text via the Action API"
```

---

## Task 5: `CategoryMembers` crawler

**Files:**
- Create: `internal/wiki/crawl.go`
- Test: `internal/wiki/crawl_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/wiki/crawl_test.go`:

```go
package wiki

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// categoryServer returns members per cmtitle. ns 14 entries are subcategories,
// ns 0 entries are pages. No continuation in this fixture.
func categoryServer(t *testing.T, byCat map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("list") != "categorymembers" {
			t.Errorf("list = %q, want categorymembers", q.Get("list"))
		}
		body, ok := byCat[q.Get("cmtitle")]
		if !ok {
			body = `{"query":{"categorymembers":[]}}`
		}
		_, _ = w.Write([]byte(body))
	}))
}

func TestCategoryMembersBFSDepthAndDedup(t *testing.T) {
	srv := categoryServer(t, map[string]string{
		"Category:Root": `{"query":{"categorymembers":[
			{"pageid":1,"ns":0,"title":"A","type":"page"},
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":0,"ns":14,"title":"Category:Sub","type":"subcat"}]}}`,
		"Category:Sub": `{"query":{"categorymembers":[
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":3,"ns":0,"title":"C","type":"page"}]}}`,
	})
	defer srv.Close()

	c := &APIClient{BaseURL: srv.URL}
	got, err := c.CategoryMembers(context.Background(), []string{"Category:Root"}, 1, 100)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	// A, B, C deduped (B appears in both); subcat itself is not a page.
	if len(got) != 3 {
		t.Fatalf("got %d members, want 3: %+v", len(got), got)
	}
	ids := map[int64]bool{}
	for _, m := range got {
		ids[m.PageID] = true
	}
	if !ids[1] || !ids[2] || !ids[3] {
		t.Errorf("missing expected page ids, got %v", ids)
	}
}

func TestCategoryMembersDepthZeroSkipsSubcats(t *testing.T) {
	srv := categoryServer(t, map[string]string{
		"Category:Root": `{"query":{"categorymembers":[
			{"pageid":1,"ns":0,"title":"A","type":"page"},
			{"pageid":0,"ns":14,"title":"Category:Sub","type":"subcat"}]}}`,
		"Category:Sub": `{"query":{"categorymembers":[{"pageid":3,"ns":0,"title":"C","type":"page"}]}}`,
	})
	defer srv.Close()

	c := &APIClient{BaseURL: srv.URL}
	got, err := c.CategoryMembers(context.Background(), []string{"Category:Root"}, 0, 100)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if len(got) != 1 || got[0].PageID != 1 {
		t.Fatalf("got %+v, want only page 1", got)
	}
}

func TestCategoryMembersRespectsMaxPages(t *testing.T) {
	srv := categoryServer(t, map[string]string{
		"Category:Root": `{"query":{"categorymembers":[
			{"pageid":1,"ns":0,"title":"A","type":"page"},
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":3,"ns":0,"title":"C","type":"page"}]}}`,
	})
	defer srv.Close()

	c := &APIClient{BaseURL: srv.URL}
	got, err := c.CategoryMembers(context.Background(), []string{"Category:Root"}, 0, 2)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want capped at 2", len(got))
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd stack/backend && go test ./internal/wiki/ -run CategoryMembers -v`
Expected: FAIL — `CategoryMembers` / `CategoryMember` undefined.

- [ ] **Step 3: Implement the crawler**

Create `internal/wiki/crawl.go`:

```go
package wiki

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// cmPageLimit is the categorymembers page size for anonymous clients (the API
// caps cmlimit at 500); the rest follow via the continuation token.
const cmPageLimit = 500

// MediaWiki namespace ids used by the crawler: main-namespace articles and the
// Category namespace (subcategory members come back tagged ns 14).
const (
	nsMain     = 0
	nsCategory = 14
)

// CategoryMember is one main-namespace article discovered while crawling a
// category: the page id to ingest and its title to fetch extracts by.
type CategoryMember struct {
	PageID int64
	Title  string
}

// CategoryMembers walks the given categories breadth-first, collecting distinct
// main-namespace articles. Subcategories are followed up to maxDepth (0 = only
// the seed categories' direct pages); page ids are deduped across the whole walk;
// the walk stops once maxPages distinct articles are collected. It follows the
// API continuation token and inherits getJSON's maxlag/Retry-After etiquette.
func (c *APIClient) CategoryMembers(ctx context.Context, categories []string, maxDepth, maxPages int) ([]CategoryMember, error) {
	type frontier struct {
		title string
		depth int
	}
	seenPages := make(map[int64]struct{})
	seenCats := make(map[string]struct{})
	var out []CategoryMember

	queue := make([]frontier, 0, len(categories))
	for _, cat := range categories {
		if _, ok := seenCats[cat]; ok {
			continue
		}
		seenCats[cat] = struct{}{}
		queue = append(queue, frontier{title: cat, depth: 0})
	}

	for len(queue) > 0 && len(out) < maxPages {
		f := queue[0]
		queue = queue[1:]

		pages, subcats, err := c.categoryMembersOf(ctx, f.title)
		if err != nil {
			return nil, err
		}
		for _, p := range pages {
			if len(out) >= maxPages {
				break
			}
			if _, ok := seenPages[p.PageID]; ok {
				continue
			}
			seenPages[p.PageID] = struct{}{}
			out = append(out, p)
		}
		if f.depth < maxDepth {
			for _, sub := range subcats {
				if _, ok := seenCats[sub]; ok {
					continue
				}
				seenCats[sub] = struct{}{}
				queue = append(queue, frontier{title: sub, depth: f.depth + 1})
			}
		}
	}
	return out, nil
}

// categoryMembersOf reads every member of one category, following continuation,
// splitting them into main-namespace pages and subcategory titles.
func (c *APIClient) categoryMembersOf(ctx context.Context, category string) (pages []CategoryMember, subcats []string, err error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("list", "categorymembers")
	params.Set("cmtitle", category)
	params.Set("cmtype", "page|subcat")
	params.Set("cmprop", "ids|title|type")
	params.Set("cmlimit", strconv.Itoa(cmPageLimit))

	for {
		var resp cmResponse
		if err := c.getJSON(ctx, params, &resp); err != nil {
			return nil, nil, err
		}
		if resp.Error != nil {
			return nil, nil, fmt.Errorf("wiki: categorymembers api error %s: %s", resp.Error.Code, resp.Error.Info)
		}
		for _, m := range resp.Query.CategoryMembers {
			switch m.NS {
			case nsMain:
				if m.PageID > 0 {
					pages = append(pages, CategoryMember{PageID: m.PageID, Title: m.Title})
				}
			case nsCategory:
				subcats = append(subcats, m.Title)
			}
		}
		if len(resp.Continue) == 0 {
			return pages, subcats, nil
		}
		for k, v := range resp.Continue {
			params.Set(k, v)
		}
	}
}

// cmResponse mirrors the categorymembers Action API response.
type cmResponse struct {
	Continue map[string]string `json:"continue"`
	Query    struct {
		CategoryMembers []cmEntry `json:"categorymembers"`
	} `json:"query"`
	Error *apiErr `json:"error"`
}

// cmEntry is one categorymembers row. NS distinguishes an article (0) from a
// subcategory (14).
type cmEntry struct {
	PageID int64  `json:"pageid"`
	NS     int    `json:"ns"`
	Title  string `json:"title"`
	Type   string `json:"type"`
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd stack/backend && go test -race ./internal/wiki/ -run CategoryMembers -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd stack/backend && gofumpt -w internal/wiki/crawl.go internal/wiki/crawl_test.go
git add internal/wiki/crawl.go internal/wiki/crawl_test.go
git commit -m "feat(wiki): CategoryMembers BFS crawler over the Action API"
```

---

## Task 6: `RunCrawl` producer logic

**Files:**
- Create: `internal/wiki/crawlproduce.go`
- Test: `internal/wiki/crawlproduce_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/wiki/crawlproduce_test.go`:

```go
package wiki

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
)

type fakeSource struct {
	members []CategoryMember
	lead    map[string]Extract
	full    map[string]Extract
}

func (f fakeSource) CategoryMembers(_ context.Context, _ []string, _, _ int) ([]CategoryMember, error) {
	return f.members, nil
}

func (f fakeSource) Extracts(_ context.Context, titles []string) ([]Extract, error) {
	return collect(f.lead, titles), nil
}

func (f fakeSource) FullExtracts(_ context.Context, titles []string) ([]Extract, error) {
	return collect(f.full, titles), nil
}

func collect(m map[string]Extract, titles []string) []Extract {
	out := make([]Extract, 0, len(titles))
	for _, t := range titles {
		if e, ok := m[t]; ok {
			out = append(out, e)
		}
	}
	return out
}

type capturePublisher struct {
	mu   sync.Mutex
	jobs []crawljob.CrawlJob
}

func (p *capturePublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	var j crawljob.CrawlJob
	if err := json.Unmarshal(body, &j); err != nil {
		return err
	}
	p.mu.Lock()
	p.jobs = append(p.jobs, j)
	p.mu.Unlock()
	return nil
}

func TestRunCrawlPublishesLeadAndBody(t *testing.T) {
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Atom"}},
		lead:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter."}},
		full:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter.\n\nAtoms bond into molecules."}},
	}
	pub := &capturePublisher{}
	stats, err := RunCrawl(context.Background(), slog.New(slog.DiscardHandler), src, pub, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "simplewiki-crawl", Project: "simplewiki",
		MaxDepth: 1, MaxPages: 100, IncludeBody: true, MaxPriority: 10,
	})
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if stats.Published != len(pub.jobs) || stats.Published == 0 {
		t.Fatalf("published = %d, jobs = %d", stats.Published, len(pub.jobs))
	}
	var kinds []string
	for _, j := range pub.jobs {
		if j.PageID != 10 || j.Corpus != "simplewiki-crawl" || j.RevisionID != 99 {
			t.Errorf("job metadata wrong: %+v", j)
		}
		kinds = append(kinds, j.Kind)
	}
	if !contains(kinds, "lead") || !contains(kinds, "body") {
		t.Errorf("kinds = %v, want both lead and body", kinds)
	}
	// chunk_index is contiguous from 0 with no gaps/dups.
	seen := map[int]bool{}
	for _, j := range pub.jobs {
		if seen[j.ChunkIndex] {
			t.Errorf("duplicate chunk_index %d", j.ChunkIndex)
		}
		seen[j.ChunkIndex] = true
	}
	for i := 0; i < len(pub.jobs); i++ {
		if !seen[i] {
			t.Errorf("missing chunk_index %d", i)
		}
	}
}

func TestRunCrawlLeadOnly(t *testing.T) {
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Atom"}},
		lead:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter."}},
	}
	pub := &capturePublisher{}
	if _, err := RunCrawl(context.Background(), slog.New(slog.DiscardHandler), src, pub, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, IncludeBody: false, MaxPriority: 10,
	}); err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	for _, j := range pub.jobs {
		if j.Kind != "lead" {
			t.Errorf("kind = %q, want lead only", j.Kind)
		}
	}
}

func TestRunCrawlSkipsMissing(t *testing.T) {
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Gone"}},
		lead:    map[string]Extract{"Gone": {Title: "Gone", Missing: true}},
	}
	pub := &capturePublisher{}
	stats, err := RunCrawl(context.Background(), slog.New(slog.DiscardHandler), src, pub, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, IncludeBody: false, MaxPriority: 10,
	})
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if stats.Published != 0 {
		t.Errorf("published = %d, want 0 for a missing page", stats.Published)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd stack/backend && go test ./internal/wiki/ -run RunCrawl -v`
Expected: FAIL — `RunCrawl`, `CrawlConfig`, `CrawlSource` undefined.

- [ ] **Step 3: Implement the producer**

Create `internal/wiki/crawlproduce.go`:

```go
package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// crawlExtractBatch is how many titles are fetched per extracts request; it
// matches the TextExtracts exlimit ceiling so every page gets its text in one
// round trip.
const crawlExtractBatch = extractsBatchMax

// CrawlSource is the MediaWiki surface the crawl producer reads: the category
// walk plus lead and (optionally) full-body extracts.
type CrawlSource interface {
	CategoryMembers(ctx context.Context, categories []string, maxDepth, maxPages int) ([]CategoryMember, error)
	Extracts(ctx context.Context, titles []string) ([]Extract, error)
	FullExtracts(ctx context.Context, titles []string) ([]Extract, error)
}

// CrawlConfig tunes a crawl run. Corpus is the provenance tag stored on every
// chunk; Project is the wiki project used to build article URLs; MaxPriority
// bounds the per-kind priority; IncludeBody adds kind=body chunks.
type CrawlConfig struct {
	Categories  []string
	Corpus      string
	Project     string
	MaxDepth    int
	MaxPages    int
	IncludeBody bool
	MaxPriority uint8
}

// CrawlStats summarizes a completed crawl run.
type CrawlStats struct {
	Pages     int
	Published int
}

// RunCrawl walks the configured categories, fetches each article's lead (and,
// when IncludeBody is set, its body), chunks them with the shared chunker, and
// publishes one self-contained CrawlJob per chunk. Chunk indices are a single
// contiguous space per page (lead chunks first, then body), so the
// (page_id, chunk_index) primary key is stable across re-crawls. It needs no
// database: every field a live row requires travels in the message. A nil logger
// falls back to slog.Default.
func RunCrawl(ctx context.Context, logger *slog.Logger, src CrawlSource, pub Publisher, cfg CrawlConfig) (CrawlStats, error) {
	if cfg.MaxPriority < 1 {
		return CrawlStats{}, fmt.Errorf("wiki: crawl needs a positive max priority, got %d", cfg.MaxPriority)
	}
	if cfg.Corpus == "" {
		return CrawlStats{}, fmt.Errorf("wiki: crawl needs a corpus tag")
	}
	if logger == nil {
		logger = slog.Default()
	}

	members, err := src.CategoryMembers(ctx, cfg.Categories, cfg.MaxDepth, cfg.MaxPages)
	if err != nil {
		return CrawlStats{}, fmt.Errorf("wiki: crawl categories: %w", err)
	}
	logger.InfoContext(ctx, "crawl collected category members",
		slog.String("corpus", cfg.Corpus), slog.Int("pages", len(members)))

	var stats CrawlStats
	stats.Pages = len(members)
	for start := 0; start < len(members); start += crawlExtractBatch {
		end := min(start+crawlExtractBatch, len(members))
		titles := make([]string, 0, end-start)
		for _, m := range members[start:end] {
			titles = append(titles, m.Title)
		}

		leads, err := src.Extracts(ctx, titles)
		if err != nil {
			return stats, fmt.Errorf("wiki: crawl lead extracts: %w", err)
		}
		full := map[string]Extract{}
		if cfg.IncludeBody {
			fulls, err := src.FullExtracts(ctx, titles)
			if err != nil {
				return stats, fmt.Errorf("wiki: crawl body extracts: %w", err)
			}
			for _, f := range fulls {
				full[f.Title] = f
			}
		}

		for _, lead := range leads {
			if lead.Missing {
				continue
			}
			published, err := publishPageChunks(ctx, pub, cfg, lead, full[lead.Title])
			if err != nil {
				return stats, err
			}
			stats.Published += published
		}
		logger.InfoContext(ctx, "crawl published page batch",
			slog.String("corpus", cfg.Corpus), slog.Int("published", stats.Published))
	}
	return stats, nil
}

// publishPageChunks chunks one page's lead and body and publishes a CrawlJob per
// chunk, assigning contiguous chunk indices (lead first). The revision id comes
// from the lead extract, falling back to the body extract.
func publishPageChunks(ctx context.Context, pub Publisher, cfg CrawlConfig, lead, full Extract) (int, error) {
	revID := lead.RevisionID
	if revID == 0 {
		revID = full.RevisionID
	}
	url := pageURL(cfg.Project, lead.Title)

	var (
		idx       int
		published int
	)
	emit := func(content string, kind domain.WikiChunkKind) error {
		job := crawljob.CrawlJob{
			PageID: lead.PageID, ChunkIndex: idx, Title: lead.Title, URL: url,
			RevisionID: revID, Corpus: cfg.Corpus, Content: content, Section: "", Kind: string(kind),
		}
		body, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("wiki: encode crawl job page %d chunk %d: %w", lead.PageID, idx, err)
		}
		if err := pub.Publish(ctx, body, priorityForKind(kind, cfg.MaxPriority)); err != nil {
			return fmt.Errorf("wiki: publish crawl job page %d chunk %d: %w", lead.PageID, idx, err)
		}
		idx++
		published++
		return nil
	}

	for _, content := range Chunk(lead.Title, lead.Text) {
		if err := emit(content, domain.WikiChunkKindLead); err != nil {
			return published, err
		}
	}
	if cfg.IncludeBody {
		for _, content := range Chunk(lead.Title, bodyText(full.Text, lead.Text)) {
			if err := emit(content, domain.WikiChunkKindBody); err != nil {
				return published, err
			}
		}
	}
	return published, nil
}

// bodyText returns the article body with the lead stripped from the front so the
// lead is not embedded twice. When the lead is not a clean prefix of the full
// text (rare formatting differences), it returns the full text unchanged.
func bodyText(full, lead string) string {
	full = strings.TrimSpace(full)
	lead = strings.TrimSpace(lead)
	if lead != "" && strings.HasPrefix(full, lead) {
		return strings.TrimSpace(full[len(lead):])
	}
	return full
}
```

> Note: `priorityForKind`, `pageURL`, `Chunk`, `Publisher`, and `Extract` already exist in package `wiki`; `RunCrawl` reuses them.

- [ ] **Step 4: Run to verify it passes**

Run: `cd stack/backend && go test -race ./internal/wiki/ -run RunCrawl -v`
Expected: PASS. Then run the whole package: `go test -race ./internal/wiki/`.

- [ ] **Step 5: Commit**

```bash
cd stack/backend && gofumpt -w internal/wiki/crawlproduce.go internal/wiki/crawlproduce_test.go
git add internal/wiki/crawlproduce.go internal/wiki/crawlproduce_test.go
git commit -m "feat(wiki): RunCrawl turns crawled pages into self-contained chunk jobs"
```

---

## Task 7: `cmd/wikicrawl` producer binary

**Files:**
- Create: `cmd/wikicrawl/main.go`
- Create: `cmd/wikicrawl/adapter.go`

- [ ] **Step 1: Implement the publisher adapter**

Create `cmd/wikicrawl/adapter.go`:

```go
package main

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

// qPublisher adapts the broker client to wiki.Publisher, so the wiki crawl
// producer never imports the transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
```

- [ ] **Step 2: Implement the binary**

Create `cmd/wikicrawl/main.go`:

```go
// Command wikicrawl walks one or more Wikipedia categories over the MediaWiki
// Action API, chunks each article's lead (and optionally body), and publishes one
// self-contained chunk job per chunk to the crawl queue, then exits. It needs no
// database: every field a live wiki_chunks row requires travels in the message,
// so the crawl worker (cmd/crawlworker) drains the queue into the corpus
// independently. The broker comes from RABBITMQ_URL; CRAWL_* selects the
// categories and shape.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("wiki crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	crawlCfg, err := config.LoadCrawl()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadCrawlQueue()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := queue.New(queue.Config{
		URL:         queueCfg.URL,
		QueueName:   queueCfg.VersionedName(),
		Version:     queueCfg.Version,
		MaxPriority: queueCfg.MaxPriority,
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	api := &wiki.APIClient{
		Corpus:     crawlCfg.Project,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}

	logger.InfoContext(ctx, "wiki crawl started",
		slog.Any("categories", crawlCfg.Categories),
		slog.String("corpus", crawlCfg.Corpus),
		slog.String("queue", queueCfg.VersionedName()),
		slog.Int("max_depth", crawlCfg.MaxDepth),
		slog.Int("max_pages", crawlCfg.MaxPages),
		slog.Bool("include_body", crawlCfg.IncludeBody))

	stats, err := wiki.RunCrawl(ctx, logger, api, qPublisher{client: client}, wiki.CrawlConfig{
		Categories:  crawlCfg.Categories,
		Corpus:      crawlCfg.Corpus,
		Project:     crawlCfg.Project,
		MaxDepth:    crawlCfg.MaxDepth,
		MaxPages:    crawlCfg.MaxPages,
		IncludeBody: crawlCfg.IncludeBody,
		MaxPriority: queueCfg.MaxPriority,
	})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "wiki crawl finished",
		slog.Int("pages", stats.Pages), slog.Int("published_chunks", stats.Published))
	return nil
}
```

- [ ] **Step 3: Verify it builds and vets**

Run: `cd stack/backend && go build ./cmd/wikicrawl/ && go vet ./cmd/wikicrawl/`
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
cd stack/backend && gofumpt -w cmd/wikicrawl/
git add cmd/wikicrawl/
git commit -m "feat(wikicrawl): category-crawl producer binary"
```

---

## Task 8: `cmd/crawlworker` consumer binary

**Files:**
- Create: `cmd/crawlworker/main.go`
- Create: `cmd/crawlworker/adapter.go`
- Test: `cmd/crawlworker/integration_test.go`

- [ ] **Step 1: Implement the adapters**

Create `cmd/crawlworker/adapter.go`:

```go
package main

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

// The adapters bridge the broker's transport types to the worker's
// transport-free interfaces, so internal/crawljob never imports internal/queue.

type qDelivery struct{ d queue.Delivery }

func (q qDelivery) Body() []byte            { return q.d.Body }
func (q qDelivery) Priority() uint8         { return q.d.Priority }
func (q qDelivery) Version() string         { return q.d.Version }
func (q qDelivery) Ack() error              { return q.d.Ack() }
func (q qDelivery) Nack(requeue bool) error { return q.d.Nack(requeue) }

type qStream struct{ client *queue.Client }

func (s qStream) Consume(ctx context.Context) (<-chan crawljob.Delivery, error) {
	raw, err := s.client.Consume(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan crawljob.Delivery)
	go func() {
		defer close(out)
		for d := range raw {
			select {
			case out <- qDelivery{d: d}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

type qEnqueuer struct{ client *queue.Client }

func (e qEnqueuer) Enqueue(ctx context.Context, body []byte, priority uint8) error {
	return e.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
```

- [ ] **Step 2: Implement the binary**

Create `cmd/crawlworker/main.go`:

```go
// Command crawlworker drains self-contained chunk jobs from the crawl queue,
// embeds each chunk's text through the Voyage API, and upserts the chunk
// (content + vector) straight into the live wiki corpus. It is a long-running
// consumer with no HTTP surface: running several replicas scales throughput, the
// broker delivers higher-priority chunks first, and a graceful SIGTERM lets
// in-flight work finish or be requeued. The broker comes from RABBITMQ_URL, the
// database from DATABASE_URL, embeddings from the Voyage API (EMBEDDING_API_KEY);
// CRAWL_WORKER_* tunes concurrency and retries.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

const (
	embedRetryBaseDelay = 1 * time.Second
	embedRetryMaxDelay  = 60 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("crawl worker exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadCrawlQueue()
	if err != nil {
		return err
	}
	embedding, err := config.LoadEmbedding()
	if err != nil {
		return err
	}
	workerCfg, err := config.LoadCrawlWorker()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	client, err := queue.New(queue.Config{
		URL:         queueCfg.URL,
		QueueName:   queueCfg.VersionedName(),
		Version:     queueCfg.Version,
		MaxPriority: queueCfg.MaxPriority,
		Prefetch:    workerCfg.Concurrency,
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	worker := crawljob.NewWorker(
		newEmbedder(logger, embedding, workerCfg),
		store,
		qStream{client: client},
		qEnqueuer{client: client},
		logger,
		crawljob.Config{Concurrency: workerCfg.Concurrency, MaxAttempts: workerCfg.MaxAttempts, KnownVersions: queueCfg.KnownVersions},
	)

	logger.InfoContext(ctx, "crawl worker started",
		slog.String("queue", queueCfg.VersionedName()),
		slog.Int("concurrency", workerCfg.Concurrency),
		slog.Int("max_attempts", workerCfg.MaxAttempts))
	if err := worker.Run(ctx); err != nil {
		return err
	}
	logger.InfoContext(ctx, "crawl worker stopped")
	return nil
}

// newEmbedder builds the Voyage embedding client wrapped in the shared retry and
// rate-limit decorators, identical to the embedworker so transient-fault handling
// is reused rather than reimplemented.
func newEmbedder(logger *slog.Logger, p config.Embedding, w config.EmbedWorker) *embed.RetryClient {
	return embed.WithRetry(
		embed.WithRateLimit(
			embed.New(embed.Config{
				APIKey:     p.APIKey,
				Model:      p.Model,
				Dim:        p.Dim,
				HTTPClient: &http.Client{Timeout: w.HTTPTimeout},
			}),
			w.RequestsPerMinute,
		),
		embed.RetryConfig{MaxAttempts: w.EmbedMaxRetries, BaseDelay: embedRetryBaseDelay, MaxDelay: embedRetryMaxDelay, Logger: logger},
	)
}
```

- [ ] **Step 3: Write the integration test**

Create `cmd/crawlworker/integration_test.go`. Model it on `cmd/embedworker/integration_test.go` and `cmd/wikisync/integration_test.go` (same `//go:build integration` tag, same env-gated broker URL `TEST_RABBITMQ_URL` and `TEST_DATABASE_URL`, same skip-if-unset helper). The test: publish two valid `crawljob.CrawlJob` messages, run the worker for a moment, then assert two rows landed in live `wiki_chunks` with non-null embeddings.

```go
//go:build integration

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// nnEmbedder returns a fixed full-dimension vector so the test needs no Voyage key.
type nnEmbedder struct{}

func (nnEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, domain.EmbeddingDim)
		v[0] = 0.5
		out[i] = v
	}
	return out, nil
}

func TestCrawlWorkerDrainsQueueIntoLive(t *testing.T) {
	// Follow the existing integration_test.go helpers in cmd/embedworker for
	// reading TEST_RABBITMQ_URL / TEST_DATABASE_URL and skipping when unset, and
	// for the throwaway-DB store setup (these tests reset wiki_chunks).
	rabbitURL := requireTestEnv(t, "TEST_RABBITMQ_URL")
	dbURL := requireTestEnv(t, "TEST_DATABASE_URL")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := postgres.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	resetWikiChunks(t, ctx, store) // helper mirrors the store package's reset

	qc, err := config.LoadCrawlQueue() // with RABBITMQ_URL set to rabbitURL via t.Setenv
	if err != nil {
		t.Fatalf("load queue cfg: %v", err)
	}
	client, err := queue.New(queue.Config{URL: rabbitURL, QueueName: qc.VersionedName(), Version: qc.Version, MaxPriority: qc.MaxPriority, Prefetch: 2})
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	defer func() { _ = client.Close() }()

	for i := 1; i <= 2; i++ {
		job := crawljob.CrawlJob{PageID: int64(100 + i), ChunkIndex: 0, Title: "T", URL: "u",
			RevisionID: 1, Corpus: "test-crawl", Content: "content", Section: "", Kind: "lead"}
		body, _ := json.Marshal(job)
		if err := client.Publish(ctx, queue.Message{Body: body, Priority: 5}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	worker := crawljob.NewWorker(nnEmbedder{}, store, qStream{client: client}, qEnqueuer{client: client},
		slog.New(slog.DiscardHandler), crawljob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: qc.KnownVersions})

	runCtx, runCancel := context.WithTimeout(ctx, 5*time.Second)
	defer runCancel()
	done := make(chan struct{})
	go func() { _ = worker.Run(runCtx); close(done) }()

	// Poll until both rows are embedded, then cancel.
	waitForEmbeddedRows(t, ctx, store, []int64{101, 102})
	runCancel()
	<-done
}
```

> The helpers `requireTestEnv`, `resetWikiChunks`, `waitForEmbeddedRows` and the `t.Setenv("RABBITMQ_URL", rabbitURL)` wiring must mirror the equivalents already used in `cmd/embedworker/integration_test.go` / `internal/store/postgres` test helpers. Reuse them; do not invent a parallel style.

- [ ] **Step 4: Verify build + the unit-level vet (integration test compiles under the tag)**

Run: `cd stack/backend && go build ./cmd/crawlworker/ && go vet -tags integration ./cmd/crawlworker/`
Expected: success. If `TEST_RABBITMQ_URL` + a throwaway `TEST_DATABASE_URL` are available, run `go test -tags integration ./cmd/crawlworker/ -run CrawlWorker -v` and expect PASS.

- [ ] **Step 5: Commit**

```bash
cd stack/backend && gofumpt -w cmd/crawlworker/
git add cmd/crawlworker/
git commit -m "feat(crawlworker): consumer binary draining crawl jobs into live"
```

---

## Task 9: Compose services + make targets

**Files:**
- Modify: `docker-compose.yml`
- Modify: `stack/backend/Makefile`, root `Makefile`

- [ ] **Step 1: Add Compose services**

In `docker-compose.yml`, under the `wiki` profile (mirror the existing `wiki-populate` and `embedworker` service blocks — same image/build, env, `depends_on: rabbitmq`, working dir). Add:

```yaml
  # crawlworker drains self-contained category-crawl chunk jobs into live
  # wiki_chunks. Scale it like embedworker:
  #   docker compose --profile wiki up -d --scale crawlworker=4 rabbitmq crawlworker
  crawlworker:
    profiles: ["wiki"]
    # (copy build/image, env_file, environment, working_dir from embedworker)
    command: ["go", "run", "./cmd/crawlworker"]
    depends_on:
      rabbitmq:
        condition: service_healthy
      postgres:
        condition: service_healthy

  # wikicrawl walks the configured categories and fills the crawl queue, then
  # exits. Run on demand:
  #   docker compose --profile wiki run --rm wikicrawl
  wikicrawl:
    profiles: ["wiki"]
    # (copy build/image, env_file, environment, working_dir from wiki-populate)
    command: ["go", "run", "./cmd/wikicrawl"]
    depends_on:
      rabbitmq:
        condition: service_healthy
```

> Copy the exact `build:`/`image:`, `env_file:`, `environment:`, `working_dir:` keys from the existing `embedworker` (for `crawlworker`) and `wiki-populate` (for `wikicrawl`) blocks so the new services share the backend image and `.env`. `wikicrawl` does not need `postgres` (DB-free); `crawlworker` does.

- [ ] **Step 2: Add make targets**

In the root `Makefile` (where `wiki-populate` / `fleet-up` style targets live — match their delegation style), add:

```makefile
crawl: ## Run the category-crawl producer once (CRAWL_CATEGORIES=... required)
	docker compose --profile wiki run --rm wikicrawl

crawl-workers: ## Start N crawl consumers (CRAWLWORKER_REPLICAS, default 2)
	docker compose --profile wiki up -d --scale crawlworker=$(or $(CRAWLWORKER_REPLICAS),2) rabbitmq crawlworker
```

> If `fleet-up`/`wiki-populate` live in `stack/backend/Makefile` instead, add them there and mirror its `docker compose` invocation exactly (it may `cd` to repo root first). Match the surrounding pattern rather than the snippet above verbatim.

- [ ] **Step 3: Verify compose config parses**

Run: `docker compose --profile wiki config >/dev/null && echo OK`
Expected: `OK` (no YAML/interpolation errors).

- [ ] **Step 4: Commit**

```bash
git add docker-compose.yml Makefile stack/backend/Makefile
git commit -m "feat(ops): compose services and make targets for category crawl"
```

---

## Task 10: Documentation

**Files:**
- Modify: `docs/ingestion-pipeline.md`

- [ ] **Step 1: Add a category-crawl section**

Add a new top-level section to `docs/ingestion-pipeline.md` (after the dump pipeline, before Cross-references) titled **"Category-crawl ingestion (additive)"** covering, in the same map-not-duplicate style as the rest of the file:

- Purpose: populate the corpus from a focused category slice over HTTP, no dump download; additive to live `wiki_chunks`.
- Topology mermaid diagram (reuse the one from the spec §3).
- Components table: `cmd/wikicrawl` (producer, DB-free), `crawl.chunks.v<n>` queue, `cmd/crawlworker` (consumer), `internal/crawljob`, `UpsertEmbeddedChunk`.
- The self-contained-message contract and why (broker can hold a primed corpus with no DB attached).
- Provenance: `CRAWL_CORPUS` tag isolates crawl rows from the dump corpus's delta checkpoint; the `(page_id, chunk_index)` PK is global.
- Config table (the `CRAWL_*` / `RABBITMQ_CRAWL_QUEUE` / `CRAWL_WORKER_*` knobs from spec §8).
- How to run (the make sequence from spec §9).
- Known limitations (section headings `''`, no producer checkpoint, re-embed cost on re-crawl) — link to spec §11.

Keep it factual and link the spec as the design source.

- [ ] **Step 2: Commit**

```bash
git add docs/ingestion-pipeline.md
git commit -m "docs: document the category-crawl ingestion path"
```

---

## Final verification

- [ ] **Step 1: Full unit suite green (race)**

Run: `cd stack/backend && go test -race ./...`
Expected: PASS (integration-tagged tests are skipped without the build tag).

- [ ] **Step 2: Integration suite green (throwaway DB + broker)**

Run (only with a throwaway `TEST_DATABASE_URL` and a `TEST_RABBITMQ_URL`):
`cd stack/backend && go test -tags integration ./internal/store/postgres/ ./cmd/crawlworker/`
Expected: PASS. Restore the dev DB afterward (`make seed`) if you reused the local broker/DB.

- [ ] **Step 3: Lint, vet, format, sqlc**

Run: `cd stack/backend && gofumpt -l . && go vet ./... && golangci-lint run ./... && make sqlc-verify`
Expected: no output / no diff.

- [ ] **Step 4: Refresh GitNexus index (it was stale)**

Run: `npx gitnexus analyze`
Expected: index updates to the new HEAD.

- [ ] **Step 5: End-to-end smoke (optional, paid — needs EMBEDDING_API_KEY)**

```bash
make fleet-up
make crawl-workers CRAWLWORKER_REPLICAS=2
make crawl CRAWL_CATEGORIES="Category:Physics" CRAWL_MAX_PAGES=20
make wiki-verify   # green = live corpus (dump + crawl) complete and consistent
make fleet-down
```

- [ ] **Step 6: Code review gate (mandatory per CLAUDE.md)**

Run a code review on the full diff, resolve every correctness finding, re-review. Only then is the work Done.
```
