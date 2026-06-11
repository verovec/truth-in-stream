package wiki

import (
	"bytes"
	"context"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// discardLogger silences a run's logs in tests that assert on behavior rather
// than on the log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

const testEmbedDim = 4

// marker derives a deterministic value from chunk content so a test can assert
// that the embedding produced for a given chunk lands on that exact chunk,
// even after concurrent sub-batch embedding reorders the work.
func marker(content string) float32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(content))
	return float32(h.Sum32() % 100000)
}

type fakeEmbedder struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), texts...))
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, testEmbedDim)
		v[0] = marker(t)
		out[i] = v
	}
	return out, nil
}

func (f *fakeEmbedder) maxBatch() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := 0
	for _, c := range f.calls {
		if len(c) > m {
			m = len(c)
		}
	}
	return m
}

// fakeBulkStore models the staging table the embed run drives: it holds staged
// chunks, fills their embeddings in place, and records the finalize call.
type fakeBulkStore struct {
	mu                sync.Mutex
	staging           []domain.WikiChunk
	remainingOverride *domain.WikiRemaining
	updated           []domain.WikiChunk
	finalizedCorpus   string
	finalizedVersion  string
	finalized         bool
	updateErr         error
}

func (f *fakeBulkStore) StagingRemaining(context.Context) (domain.WikiRemaining, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remainingOverride != nil {
		return *f.remainingOverride, nil
	}
	rem := domain.WikiRemaining{}
	pages := map[int64]struct{}{}
	for _, c := range f.staging {
		if c.Embedding != nil {
			continue
		}
		rem.Chunks++
		rem.Chars += int64(len(c.Content))
		pages[c.PageID] = struct{}{}
	}
	rem.Pages = int64(len(pages))
	return rem, nil
}

func (f *fakeBulkStore) UnembeddedStaging(_ context.Context, limit int) ([]domain.WikiChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.WikiChunk
	for _, c := range f.staging {
		if c.Embedding == nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PageID != out[j].PageID {
			return out[i].PageID < out[j].PageID
		}
		return out[i].ChunkIndex < out[j].ChunkIndex
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeBulkStore) UpdateStagingEmbeddings(_ context.Context, chunks []domain.WikiChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = append(f.updated, chunks...)
	for _, c := range chunks {
		for i := range f.staging {
			if f.staging[i].PageID == c.PageID && f.staging[i].ChunkIndex == c.ChunkIndex {
				f.staging[i].Embedding = c.Embedding
			}
		}
	}
	return nil
}

func (f *fakeBulkStore) FinalizeStaging(_ context.Context, corpus, version string, _ time.Time, _ string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalizedCorpus = corpus
	f.finalizedVersion = version
	f.finalized = true
	return nil
}

// sampleChunks builds chunks for pages numbered 1..pages, two chunks per page,
// with NULL embeddings - the state a freshly built staging table is in.
func sampleChunks(pages int) []domain.WikiChunk {
	const perPage = 2
	var out []domain.WikiChunk
	for p := 1; p <= pages; p++ {
		for i := range perPage {
			out = append(out, domain.WikiChunk{
				PageID:     int64(p),
				ChunkIndex: i,
				Title:      "T",
				URL:        "https://simple.wikipedia.org/wiki/T",
				RevisionID: 1,
				Corpus:     "simplewiki",
				Content:    contentFor(p, i),
			})
		}
	}
	return out
}

func contentFor(page, idx int) string {
	return "page-" + string(rune('A'+page)) + "-chunk-" + string(rune('0'+idx))
}

func testConfig() Config {
	return Config{Corpus: "simplewiki", DumpVersion: "Mon, 01 Jun 2026 00:00:00 GMT", BatchSize: 2, Concurrency: 2, MaintenanceWorkMem: "64MB", MaxParallelWorkers: 0}
}

func TestEstimateBulkEmbed(t *testing.T) {
	t.Parallel()
	src := &fakeBulkStore{remainingOverride: &domain.WikiRemaining{Pages: 10, Chunks: 100, Chars: 5000}}

	est, err := EstimateBulkEmbed(t.Context(), src)
	if err != nil {
		t.Fatalf("EstimateBulkEmbed: %v", err)
	}
	// 5000 chars / 5 chars-per-token = 1000 tokens; 1000/1e6 * $0.12 = $1.2e-4.
	if est.Pages != 10 || est.Chunks != 100 || est.Tokens != 1000 {
		t.Errorf("estimate counts = %+v, want pages 10 chunks 100 tokens 1000", est)
	}
	if est.CostUSD < 1.19e-4 || est.CostUSD > 1.21e-4 {
		t.Errorf("cost = %v, want ~1.2e-4", est.CostUSD)
	}
}

func TestRunBulkEmbedEmbedsAllInOrder(t *testing.T) {
	t.Parallel()
	store := &fakeBulkStore{staging: sampleChunks(5)}
	embedder := &fakeEmbedder{}

	stats, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}
	if stats.Embedded != 10 {
		t.Errorf("embedded = %d, want 10", stats.Embedded)
	}
	if !store.finalized || store.finalizedCorpus != "simplewiki" {
		t.Errorf("finalized = %v corpus %q, want true simplewiki", store.finalized, store.finalizedCorpus)
	}
	if store.finalizedVersion != testConfig().DumpVersion {
		t.Errorf("finalized version = %q, want %q", store.finalizedVersion, testConfig().DumpVersion)
	}
	if len(store.updated) != 10 {
		t.Fatalf("updated %d chunks, want 10", len(store.updated))
	}
	for i, c := range store.updated {
		if i > 0 {
			prev := store.updated[i-1]
			if c.PageID < prev.PageID || (c.PageID == prev.PageID && c.ChunkIndex <= prev.ChunkIndex) {
				t.Fatalf("updated out of order at %d: %v after %v", i, c, prev)
			}
		}
		if len(c.Embedding) != testEmbedDim {
			t.Errorf("chunk %d embedding dim = %d, want %d", i, len(c.Embedding), testEmbedDim)
		}
		if c.Embedding[0] != marker(c.Content) {
			t.Errorf("chunk %d embedding mismapped: got %v want %v", i, c.Embedding[0], marker(c.Content))
		}
	}
	if mb := embedder.maxBatch(); mb > 2 {
		t.Errorf("max embed batch = %d, want <= BatchSize 2", mb)
	}
}

func TestRunBulkEmbedLogsBatchProgress(t *testing.T) {
	t.Parallel()
	// 5 pages * 2 chunks = 10 chunks; BatchSize 2 => 5 HTTP batches, each logged.
	store := &fakeBulkStore{staging: sampleChunks(5)}
	embedder := &fakeEmbedder{}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if _, err := RunBulkEmbed(t.Context(), logger, store, embedder, testConfig()); err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}

	var (
		sawStart, sawFinalize bool
		batchLines, maxDone   int64
	)
	for line := range bytes.Lines(buf.Bytes()) {
		var rec struct {
			Msg           string `json:"msg"`
			PendingTotal  int64  `json:"pending_total"`
			Embedded      int64  `json:"embedded"`
			PendingChunks int64  `json:"pending_chunks"`
			EmbedDuration *int64 `json:"embed_duration"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		switch rec.Msg {
		case "starting bulk embed":
			sawStart = true
			if rec.PendingChunks != 10 {
				t.Errorf("start line pending_chunks = %d, want 10", rec.PendingChunks)
			}
		case "embedded wiki chunk batch":
			batchLines++
			if rec.PendingTotal != 10 {
				t.Errorf("batch line pending_total = %d, want 10", rec.PendingTotal)
			}
			if rec.EmbedDuration == nil {
				t.Error("batch line missing embed_duration; per-batch embed latency must be logged")
			}
			maxDone = max(maxDone, rec.Embedded)
		case "bulk embed finalized; wiki_chunks now serves the embedded corpus":
			sawFinalize = true
		}
	}

	if !sawStart {
		t.Error("missing the start-of-run log line")
	}
	if batchLines != 5 {
		t.Errorf("per-batch log lines = %d, want 5 (one per HTTP batch)", batchLines)
	}
	if maxDone != 10 {
		t.Errorf("cumulative embedded peaked at %d, want 10", maxDone)
	}
	if !sawFinalize {
		t.Error("missing the finalize log line")
	}
}

func TestRunBulkEmbedResumeEmbedsOnlyRemaining(t *testing.T) {
	t.Parallel()
	// Staging already holds page 1 (both chunks) embedded from an interrupted
	// prior run; only the rest is left.
	staging := sampleChunks(3)
	for i := range staging {
		if staging[i].PageID == 1 {
			staging[i].Embedding = []float32{1, 2, 3, 4}
		}
	}
	store := &fakeBulkStore{staging: staging}
	embedder := &fakeEmbedder{}

	stats, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed (resume): %v", err)
	}
	// 6 chunks total, 2 already embedded => 4 remaining.
	if stats.Embedded != 4 {
		t.Errorf("embedded = %d, want 4 (resume skips the staged prefix)", stats.Embedded)
	}
	if len(store.updated) != 4 {
		t.Fatalf("updated %d, want 4", len(store.updated))
	}
	first := store.updated[0]
	if first.PageID != 2 || first.ChunkIndex != 0 {
		t.Errorf("first resumed chunk = (%d,%d), want (2,0)", first.PageID, first.ChunkIndex)
	}
	if !store.finalized {
		t.Error("resume must still finalize and swap")
	}
}

func TestRunBulkEmbedFullyStagedStillFinalizes(t *testing.T) {
	t.Parallel()
	// Every chunk was embedded before a prior run died at finalize; this run
	// embeds nothing but must still build, swap, and never re-embed.
	staging := sampleChunks(2)
	for i := range staging {
		staging[i].Embedding = []float32{1, 2, 3, 4}
	}
	store := &fakeBulkStore{staging: staging}
	embedder := &fakeEmbedder{}

	stats, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}
	if stats.Embedded != 0 {
		t.Errorf("embedded = %d, want 0 (everything was already staged)", stats.Embedded)
	}
	if len(embedder.calls) != 0 {
		t.Errorf("embedder called %d times, want 0", len(embedder.calls))
	}
	if !store.finalized {
		t.Error("a fully-staged resume must still finalize and swap")
	}
}
