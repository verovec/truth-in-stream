package wiki

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// memCheckpoint is an in-memory Checkpoint for behavior tests: it records resolved
// pages without touching disk and remembers whether Clear was called.
type memCheckpoint struct {
	mu      sync.Mutex
	done    map[int64]struct{}
	cleared bool
}

func newMemCheckpoint(seed ...int64) *memCheckpoint {
	c := &memCheckpoint{done: make(map[int64]struct{})}
	c.MarkDone(seed...)
	return c
}

func (c *memCheckpoint) Done(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.done[id]
	return ok
}

func (c *memCheckpoint) MarkDone(ids ...int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		c.done[id] = struct{}{}
	}
}

func (c *memCheckpoint) Save() error { return nil }

func (c *memCheckpoint) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done = make(map[int64]struct{})
	c.cleared = true
	return nil
}

func (c *memCheckpoint) wasCleared() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cleared
}

// TestRunCrawlResumesFromCheckpoint proves a page already resolved on a previous
// run is skipped: it is neither gated (no repeated LLM spend) nor published.
func TestRunCrawlResumesFromCheckpoint(t *testing.T) {
	t.Parallel()
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Atom"}, {PageID: 20, Title: "Bohr"}},
		lead: map[string]Extract{
			"Atom": {PageID: 10, Title: "Atom", RevisionID: 1, Text: "Atoms are matter."},
			"Bohr": {PageID: 20, Title: "Bohr", RevisionID: 1, Text: "Bohr modeled the atom."},
		},
	}
	var gated sync.Map
	gate := gateFunc(func(_ context.Context, passage string) (bool, error) {
		gated.Store(passage, true)
		return true, nil
	})
	pub := &capturePublisher{}
	cp := newMemCheckpoint(10) // page 10 already done

	cfg := CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, MaxPriority: 10, GateConcurrency: 2, Checkpoint: cp,
	}
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, gate, cfg)
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}

	if _, ok := gated.Load("Atoms are matter."); ok {
		t.Fatal("resumed page 10 was re-gated; a resume must not re-spend the gate on completed pages")
	}
	for _, j := range pub.jobs {
		if j.PageID == 10 {
			t.Fatal("resumed page 10 was re-published")
		}
	}
	if len(pub.jobs) != 1 || pub.jobs[0].PageID != 20 {
		t.Fatalf("published %d jobs, want only page 20's chunk", len(pub.jobs))
	}
	if stats.Published != 1 {
		t.Fatalf("published = %d, want 1", stats.Published)
	}
	// The run completed, so the checkpoint is cleared: the next scheduled run starts
	// fresh and re-crawls every page (catching updates) rather than skipping them.
	if !cp.wasCleared() {
		t.Fatal("completed run did not clear the checkpoint; the next run would skip everything")
	}
	if cp.Done(20) || cp.Done(10) {
		t.Fatal("checkpoint still reports pages done after a completed run's clear")
	}
}

// extractErrorSource errors its lead extracts for any batch containing failTitle,
// standing in for an upstream that fails one batch of a multi-batch crawl.
type extractErrorSource struct {
	members   []CategoryMember
	lead      map[string]Extract
	failTitle string
}

func (s extractErrorSource) CategoryMembers(_ context.Context, _ []string, _, _ int) ([]CategoryMember, error) {
	return s.members, nil
}

func (s extractErrorSource) Extracts(_ context.Context, titles []string) ([]Extract, error) {
	if contains(titles, s.failTitle) {
		return nil, errors.New("upstream extract failure")
	}
	return collect(s.lead, titles), nil
}

func (s extractErrorSource) FullExtracts(_ context.Context, _ []string) ([]Extract, error) {
	return nil, nil
}

// twoBatchSource builds a source with one failing batch (its first page is the
// fail marker) of exactly extractsBatchMax pages, followed by a batch of good
// pages, so a run must skip the first whole batch and finish the second.
func twoBatchSource() extractErrorSource {
	members := []CategoryMember{{PageID: 1, Title: "Boom"}}
	lead := map[string]Extract{}
	// Fill the first batch (extractsBatchMax = 20) exactly: "Boom" plus 19 more.
	for i := 2; i <= 20; i++ {
		title := fmt.Sprintf("First%d", i)
		members = append(members, CategoryMember{PageID: int64(i), Title: title})
	}
	for i := 100; i < 105; i++ {
		title := fmt.Sprintf("Good%d", i)
		members = append(members, CategoryMember{PageID: int64(i), Title: title})
		lead[title] = Extract{PageID: int64(i), Title: title, RevisionID: 1, Text: "A good fact."}
	}
	return extractErrorSource{members: members, lead: lead, failTitle: "Boom"}
}

func TestRunCrawlSkipsFailedBatchWithinBudget(t *testing.T) {
	t.Parallel()
	src := twoBatchSource()
	pub := &capturePublisher{}
	cfg := CrawlConfig{
		Categories: []string{"c"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, MaxPriority: 10, ErrorBudget: 50, // budget covers the 19 skipped first-batch pages
	}
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, nil, cfg)
	if err != nil {
		t.Fatalf("RunCrawl within budget should not fail: %v", err)
	}
	if stats.Skipped != 20 {
		t.Fatalf("skipped = %d, want 20 (the whole failing first batch)", stats.Skipped)
	}
	if stats.Published != 5 {
		t.Fatalf("published = %d, want 5 (the good second batch)", stats.Published)
	}
}

func TestRunCrawlAbortsOnCanceledContext(t *testing.T) {
	t.Parallel()
	// A canceled context must abort with an error, not be charged to the budget and
	// masked as a partial success.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := twoBatchSource()
	cfg := CrawlConfig{
		Categories: []string{"c"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, MaxPriority: 10, ErrorBudget: 1000, // huge budget: only cancellation should fail it
	}
	if _, err := RunCrawl(ctx, slog.New(slog.DiscardHandler), src, &capturePublisher{}, nil, cfg); err == nil {
		t.Fatal("a canceled crawl returned success instead of propagating the cancellation")
	}
}

func TestRunCrawlFailsWhenErrorBudgetExhausted(t *testing.T) {
	t.Parallel()
	src := twoBatchSource()
	pub := &capturePublisher{}
	cfg := CrawlConfig{
		Categories: []string{"c"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, MaxPriority: 10, ErrorBudget: 5, // 19 skipped pages blow the budget
	}
	_, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, nil, cfg)
	if err == nil {
		t.Fatal("RunCrawl did not fail after exhausting the error budget")
	}
	if !strings.Contains(err.Error(), "error budget exhausted") {
		t.Fatalf("error = %v, want an error-budget-exhausted message", err)
	}
	if !strings.Contains(err.Error(), "Boom") {
		t.Fatalf("error = %v, want the skipped page names in the summary", err)
	}
}

// TestRunCrawlGateFailClosedHoldsChunk proves fail-closed drops a chunk whose gate
// call errored and counts its page as unresolved (Skipped), so with headroom in the
// error budget the run still completes but the page is not published.
func TestRunCrawlGateFailClosedHoldsChunk(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	gate := gateFunc(func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("model unavailable")
	})
	cp := newMemCheckpoint()
	cfg := gatedCrawlConfig()
	cfg.GateFailMode = GateFailClosed
	cfg.ErrorBudget = 10 // one held page is within budget
	cfg.Checkpoint = cp

	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), pub, gate, cfg)
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if len(pub.jobs) != 0 {
		t.Fatalf("fail-closed published %d chunks, want 0 (all held on gate error)", len(pub.jobs))
	}
	if stats.Published != 0 {
		t.Fatalf("published = %d, want 0", stats.Published)
	}
	if stats.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the held page charged to the budget)", stats.Skipped)
	}
}

// TestRunCrawlGateFailClosedExhaustsBudgetWhenGateBroken proves a persistently
// broken gate under fail-closed fails the run loudly (budget exhausted by held
// pages) instead of silently re-gating forever.
func TestRunCrawlGateFailClosedExhaustsBudgetWhenGateBroken(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	gate := gateFunc(func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("model unavailable")
	})
	cfg := gatedCrawlConfig()
	cfg.GateFailMode = GateFailClosed
	cfg.ErrorBudget = 0 // no headroom: the first held page fails the run

	_, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), pub, gate, cfg)
	if err == nil {
		t.Fatal("a broken fail-closed gate did not fail the run; it would re-gate forever")
	}
	if !strings.Contains(err.Error(), "error budget exhausted") {
		t.Fatalf("error = %v, want an error-budget-exhausted signal", err)
	}
}

// TestRunCrawlGateFailClosedHoldsPageWithAnErroredChunk proves that when only one
// of a page's chunks hits a gate error under fail-closed, the clean chunk still
// publishes but the page is not checkpointed, so the whole page retries and the
// held chunk is re-judged next run.
func TestRunCrawlGateFailClosedHoldsPageWithAnErroredChunk(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	// The body chunk ("Dropme") errors; the lead chunk ("Keepme") is judged cleanly.
	gate := gateFunc(func(_ context.Context, passage string) (bool, error) {
		if strings.Contains(passage, "Dropme") {
			return false, errors.New("model unavailable")
		}
		return true, nil
	})
	cfg := gatedCrawlConfig()
	cfg.GateFailMode = GateFailClosed
	cfg.ErrorBudget = 10 // the held page is within budget

	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), pub, gate, cfg)
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	// The clean lead chunk published, but the page had a held chunk, so the page is
	// counted unresolved (Skipped) and not checkpointed: the whole page retries so
	// the held chunk is re-judged.
	if len(pub.jobs) != 1 || pub.jobs[0].Kind != "lead" {
		t.Fatalf("published %d jobs, want only the clean lead chunk", len(pub.jobs))
	}
	if stats.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the page with a held chunk is unresolved)", stats.Skipped)
	}
}
