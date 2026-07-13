package embedjob

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// budgetEmbedder simulates Voyage's per-request token ceiling: a call whose
// estimated tokens exceed maxTokens fails like an HTTP 400 (maxTokens <= 0 never
// fails), any call at or under it succeeds. It records each call's input size so
// a test can prove the worker split an oversized batch rather than thrashing one
// chunk at a time.
type budgetEmbedder struct {
	maxTokens int
	mu        sync.Mutex
	sizes     []int
}

func (e *budgetEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	tokens := 0
	for _, t := range texts {
		tokens += estimateTokens(t)
	}
	e.mu.Lock()
	e.sizes = append(e.sizes, len(texts))
	e.mu.Unlock()
	if e.maxTokens > 0 && tokens > e.maxTokens {
		return nil, errors.New("voyage: api status 400: Total number of tokens in the batch exceeds the limit")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = testVec(1)
	}
	return out, nil
}

func (e *budgetEmbedder) callSizes() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.sizes...)
}

// makeDeliveries builds n distinct valid deliveries whose content is contentLen
// characters, so each carries a known estimated token count.
func makeDeliveries(t *testing.T, n, contentLen int) []Delivery {
	t.Helper()
	content := strings.Repeat("a", contentLen)
	out := make([]Delivery, n)
	for i := range n {
		out[i] = &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: strconv.Itoa(i), ChunkIndex: 0, Content: content}), priority: 1}
	}
	return out
}

// TestProcessBatchSplitsOnTokenBudget proves the worker proactively splits a
// batch that would exceed the token budget, so no provider call is sent over the
// budget and every chunk still embeds in a batch (not one per call).
func TestProcessBatchSplitsOnTokenBudget(t *testing.T) {
	t.Parallel()
	// content of 20 chars -> estimateTokens = 20/5 + 1 = 5 tokens per chunk.
	emb := &budgetEmbedder{} // provider never fails; only the proactive budget splits
	st := &fakeStore{updated: true}
	// Budget of 12 tokens fits two 5-token chunks (10) but not three (15).
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3, MaxBatchTokens: 12})

	deliveries := makeDeliveries(t, 8, 20)
	w.processBatch(t.Context(), deliveries)

	if got := len(st.recorded()); got != 8 {
		t.Fatalf("store writes = %d, want 8 (every chunk embedded)", got)
	}
	for i, d := range deliveries {
		if acked, nacked, _ := d.(*recDelivery).state(); !acked || nacked {
			t.Fatalf("delivery %d acked=%v nacked=%v, want acked", i, acked, nacked)
		}
	}
	sizes := emb.callSizes()
	if len(sizes) < 2 {
		t.Fatalf("embed calls = %v, want a split (more than one call)", sizes)
	}
	for _, s := range sizes {
		if s > 2 {
			t.Errorf("a provider call embedded %d chunks (%d tokens), over the 12-token budget; sizes=%v", s, s*5, sizes)
		}
	}
	if w.Stats().Processed != 8 {
		t.Errorf("Stats.Processed = %d, want 8", w.Stats().Processed)
	}
}

// TestProcessBatchRecoversFromSizeError proves an under-estimated batch that the
// provider rejects for size is recovered by halving, never degraded to a
// per-chunk sweep, and every chunk still lands embedded and acked.
func TestProcessBatchRecoversFromSizeError(t *testing.T) {
	t.Parallel()
	// content 20 chars -> 5 tokens/chunk. Provider rejects any call over 12 tokens,
	// but the worker's own budget is left large so nothing splits proactively - the
	// recovery must come from splitting on the provider's size error.
	emb := &budgetEmbedder{maxTokens: 12}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3, MaxBatchTokens: 1_000_000})

	deliveries := makeDeliveries(t, 8, 20)
	w.processBatch(t.Context(), deliveries)

	if got := len(st.recorded()); got != 8 {
		t.Fatalf("store writes = %d, want 8 (every chunk embedded after recovery)", got)
	}
	for i, d := range deliveries {
		if acked, nacked, _ := d.(*recDelivery).state(); !acked || nacked {
			t.Fatalf("delivery %d acked=%v nacked=%v, want acked (recovered, not dead-lettered)", i, acked, nacked)
		}
	}
	// A successful call embedded more than one chunk, proving the split stopped at a
	// whole sub-batch rather than collapsing to one call per chunk.
	sizes := emb.callSizes()
	maxOK := 0
	for _, s := range sizes {
		if s*5 <= 12 && s > maxOK {
			maxOK = s
		}
	}
	if maxOK < 2 {
		t.Errorf("no provider call embedded a whole sub-batch (>1 chunk); the split degraded to per-chunk; sizes=%v", sizes)
	}
	if w.Stats().Processed != 8 {
		t.Errorf("Stats.Processed = %d, want 8", w.Stats().Processed)
	}
}

// TestProcessBatchOversizedSingleChunkRetries proves a lone chunk the provider
// rejects even on its own is retried (bounded), not dropped or looped: the split
// bottoms out at one input and hands it to the retry path.
func TestProcessBatchOversizedSingleChunkRetries(t *testing.T) {
	t.Parallel()
	// Provider rejects everything (maxTokens=1, every chunk is >1 token).
	emb := &budgetEmbedder{maxTokens: 1}
	st := &fakeStore{updated: true}
	w := NewWorker(emb, st, nil, &recEnqueuer{}, slog.New(slog.DiscardHandler), Config{Concurrency: 1, MaxAttempts: 3, MaxBatchTokens: 1_000_000})

	deliveries := makeDeliveries(t, 1, 20)
	w.processBatch(t.Context(), deliveries)

	// The single chunk cannot embed, so it is re-enqueued for a bounded retry
	// (attempt < max), acked after the re-enqueue - never nacked-for-requeue-forever.
	acked, nacked, _ := deliveries[0].(*recDelivery).state()
	if !acked || nacked {
		t.Fatalf("oversized single chunk acked=%v nacked=%v, want acked after re-enqueue for retry", acked, nacked)
	}
	if len(st.recorded()) != 0 {
		t.Fatalf("store writes = %d, want 0 (nothing embedded)", len(st.recorded()))
	}
}

// TestProcessBatchStatsCountsParked proves the drain counters separate acked
// (processed) deliveries from dead-lettered (parked) ones, the counts the
// consumer run alert reports.
func TestProcessBatchStatsCountsParked(t *testing.T) {
	t.Parallel()
	emb := &budgetEmbedder{}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3, KnownVersions: []string{"1"}})

	good1 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "hello"}), version: "1"}
	good2 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "2", ChunkIndex: 0, Content: "world"}), version: "1"}
	poison := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "3", ChunkIndex: 0, Content: "x"}), version: "99"}

	w.processBatch(t.Context(), []Delivery{good1, good2, poison})

	stats := w.Stats()
	if stats.Processed != 2 {
		t.Errorf("Stats.Processed = %d, want 2", stats.Processed)
	}
	if stats.ParkedToDLQ != 1 {
		t.Errorf("Stats.ParkedToDLQ = %d, want 1 (the unknown-version delivery)", stats.ParkedToDLQ)
	}
}

func TestEstimateTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want int
	}{
		{"", 1},
		{"abcde", 2},
		{strings.Repeat("a", 100), 21},
	}
	for _, tc := range tests {
		if got := estimateTokens(tc.text); got != tc.want {
			t.Errorf("estimateTokens(%d chars) = %d, want %d", len(tc.text), got, tc.want)
		}
	}
}
