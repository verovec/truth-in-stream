package wiki

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"sort"
	"sync"
	"testing"

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

type fakeEmbedStore struct {
	mu        sync.Mutex
	live      []domain.WikiChunk
	watermark domain.WikiCursor
	remaining domain.WikiRemaining
	created   bool
	copied    []domain.WikiChunk
	finalized string
	copyErr   error
}

func (f *fakeEmbedStore) CreateStaging(context.Context) error {
	f.created = true
	return nil
}

func (f *fakeEmbedStore) EmbedWatermark(context.Context) (domain.WikiCursor, error) {
	return f.watermark, nil
}

func (f *fakeEmbedStore) UnembeddedChunks(_ context.Context, cur domain.WikiCursor, limit int) ([]domain.WikiChunk, error) {
	var out []domain.WikiChunk
	for _, c := range f.live {
		if afterCursor(c, cur) {
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

func (f *fakeEmbedStore) EstimateRemaining(context.Context, domain.WikiCursor) (domain.WikiRemaining, error) {
	return f.remaining, nil
}

func (f *fakeEmbedStore) CopyStagingChunks(_ context.Context, chunks []domain.WikiChunk) error {
	if f.copyErr != nil {
		return f.copyErr
	}
	f.mu.Lock()
	f.copied = append(f.copied, chunks...)
	f.mu.Unlock()
	return nil
}

func (f *fakeEmbedStore) FinalizeStaging(_ context.Context, corpus, _ string, _ int) error {
	f.finalized = corpus
	return nil
}

func afterCursor(c domain.WikiChunk, cur domain.WikiCursor) bool {
	if c.PageID != cur.PageID {
		return c.PageID > cur.PageID
	}
	return int32(c.ChunkIndex) > cur.ChunkIndex
}

// sampleChunks builds chunks for pages numbered 1..pages, two chunks per page.
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
	return Config{Corpus: "simplewiki", BatchSize: 2, Concurrency: 2, MaintenanceWorkMem: "64MB", MaxParallelWorkers: 0}
}

func TestEstimateBulkEmbed(t *testing.T) {
	t.Parallel()
	src := &fakeEmbedStore{remaining: domain.WikiRemaining{Pages: 10, Chunks: 100, Chars: 5000}}

	est, err := EstimateBulkEmbed(t.Context(), src)
	if err != nil {
		t.Fatalf("EstimateBulkEmbed: %v", err)
	}
	// 5000 chars / 5 chars-per-token = 1000 tokens; 1000/1e6 * $0.06 = $6e-5.
	if est.Pages != 10 || est.Chunks != 100 || est.Tokens != 1000 {
		t.Errorf("estimate counts = %+v, want pages 10 chunks 100 tokens 1000", est)
	}
	if est.CostUSD < 5.9e-5 || est.CostUSD > 6.1e-5 {
		t.Errorf("cost = %v, want ~6e-5", est.CostUSD)
	}
}

func TestRunBulkEmbedEmbedsAllInOrder(t *testing.T) {
	t.Parallel()
	store := &fakeEmbedStore{live: sampleChunks(5)}
	embedder := &fakeEmbedder{}

	stats, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}
	if stats.Embedded != 10 {
		t.Errorf("embedded = %d, want 10", stats.Embedded)
	}
	if !store.created {
		t.Error("staging table was not created")
	}
	if store.finalized != "simplewiki" {
		t.Errorf("finalized corpus = %q, want simplewiki", store.finalized)
	}
	if len(store.copied) != 10 {
		t.Fatalf("copied %d chunks, want 10", len(store.copied))
	}
	for i, c := range store.copied {
		if i > 0 {
			prev := store.copied[i-1]
			if c.PageID < prev.PageID || (c.PageID == prev.PageID && c.ChunkIndex <= prev.ChunkIndex) {
				t.Fatalf("copied out of order at %d: %v after %v", i, c, prev)
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
	store := &fakeEmbedStore{live: sampleChunks(5), remaining: domain.WikiRemaining{Chunks: 10}}
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
			ResumeAfter   int64  `json:"resume_after_page"`
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
			if rec.ResumeAfter != 0 {
				t.Errorf("start line resume_after_page = %d, want 0 on a fresh run", rec.ResumeAfter)
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

func TestRunBulkEmbedResumesFromWatermark(t *testing.T) {
	t.Parallel()
	// Staging already holds page 1 (both chunks) and page 2 chunk 0.
	store := &fakeEmbedStore{
		live:      sampleChunks(3),
		watermark: domain.WikiCursor{PageID: 2, ChunkIndex: 0},
	}
	embedder := &fakeEmbedder{}

	stats, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}
	// Remaining after (2,0): (2,1), (3,0), (3,1) = 3 chunks.
	if stats.Embedded != 3 {
		t.Errorf("embedded = %d, want 3 (resume skips the staged prefix)", stats.Embedded)
	}
	if len(store.copied) != 3 {
		t.Fatalf("copied %d, want 3", len(store.copied))
	}
	first := store.copied[0]
	if first.PageID != 2 || first.ChunkIndex != 1 {
		t.Errorf("first resumed chunk = (%d,%d), want (2,1)", first.PageID, first.ChunkIndex)
	}
	if store.finalized != "simplewiki" {
		t.Error("resume must still finalize and swap")
	}
}

func TestRunBulkEmbedResumeWithEverythingStagedStillFinalizes(t *testing.T) {
	t.Parallel()
	// A prior run staged every chunk before dying; this run finds nothing left
	// to embed but must still build, swap, and never recreate staging.
	store := &fakeEmbedStore{
		live:      sampleChunks(2),
		watermark: domain.WikiCursor{PageID: 2, ChunkIndex: 1},
	}
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
	if store.created {
		t.Error("must not recreate staging when resuming an existing one")
	}
	if store.finalized != "simplewiki" {
		t.Error("a fully-staged resume must still finalize and swap")
	}
}

func TestRunBulkEmbedNoChunksSkipsFinalize(t *testing.T) {
	t.Parallel()
	store := &fakeEmbedStore{}
	embedder := &fakeEmbedder{}

	stats, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}
	if stats.Embedded != 0 {
		t.Errorf("embedded = %d, want 0", stats.Embedded)
	}
	if store.finalized != "" {
		t.Error("an empty corpus must not be swapped")
	}
	if len(embedder.calls) != 0 {
		t.Errorf("embedder called %d times for empty corpus, want 0", len(embedder.calls))
	}
}

func TestRunBulkEmbedPropagatesEmbedError(t *testing.T) {
	t.Parallel()
	store := &fakeEmbedStore{live: sampleChunks(2)}
	embedder := &fakeEmbedder{err: errors.New("provider down")}

	if _, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig()); err == nil {
		t.Fatal("want embed error, got nil")
	}
	if store.finalized != "" {
		t.Error("must not finalize after an embed failure")
	}
	if len(store.copied) != 0 {
		t.Errorf("copied %d chunks despite embed failure, want 0", len(store.copied))
	}
}

func TestRunBulkEmbedPropagatesCopyError(t *testing.T) {
	t.Parallel()
	store := &fakeEmbedStore{live: sampleChunks(2), copyErr: errors.New("copy failed")}
	embedder := &fakeEmbedder{}

	if _, err := RunBulkEmbed(t.Context(), discardLogger(), store, embedder, testConfig()); err == nil {
		t.Fatal("want copy error, got nil")
	}
	if store.finalized != "" {
		t.Error("must not finalize after a copy failure")
	}
}
