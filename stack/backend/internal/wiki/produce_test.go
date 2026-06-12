package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
)

// fakePublisher records every published job so a test can assert the producer
// enqueued one prioritized job per un-embedded chunk.
type fakePublisher struct {
	mu   sync.Mutex
	msgs []publishedMsg
	err  error
}

type publishedMsg struct {
	body     []byte
	priority uint8
}

func (p *fakePublisher) Publish(_ context.Context, body []byte, priority uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.msgs = append(p.msgs, publishedMsg{body: append([]byte(nil), body...), priority: priority})
	return nil
}

func (p *fakePublisher) published() []publishedMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publishedMsg(nil), p.msgs...)
}

// fakeProducerStore models the staging table the producer drives: it serves the
// un-embedded chunks to enqueue and a programmed sequence of remaining counts
// that the drain wait polls until it reaches zero (the fleet finished).
type fakeProducerStore struct {
	mu sync.Mutex

	staging []domain.WikiChunk

	// remaining is the sequence StagingRemaining returns on successive calls; the
	// last value is held once the sequence is exhausted, modeling a fleet that
	// has reached a steady state (drained at 0, or stalled at a positive count).
	remaining []int64
	remIdx    int

	finalized        bool
	finalizedCorpus  string
	finalizedVersion string

	unembedErr error
	remErr     error
	finalErr   error
}

func (f *fakeProducerStore) CountUnembeddedStaging(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remErr != nil {
		return 0, f.remErr
	}
	var n int64
	switch {
	case len(f.remaining) == 0:
		n = 0
	case f.remIdx < len(f.remaining):
		n = f.remaining[f.remIdx]
		f.remIdx++
	default:
		n = f.remaining[len(f.remaining)-1]
	}
	return n, nil
}

func (f *fakeProducerStore) UnembeddedStaging(_ context.Context, after domain.WikiCursor, limit int) ([]domain.WikiChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unembedErr != nil {
		return nil, f.unembedErr
	}
	out := []domain.WikiChunk{}
	for _, c := range f.staging {
		if c.PageID > after.PageID || (c.PageID == after.PageID && int32(c.ChunkIndex) > after.ChunkIndex) {
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

func (f *fakeProducerStore) FinalizeStaging(_ context.Context, corpus, version string, _ time.Time, _ string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finalErr != nil {
		return f.finalErr
	}
	f.finalized = true
	f.finalizedCorpus = corpus
	f.finalizedVersion = version
	return nil
}

func producerChunks(pages int) []domain.WikiChunk {
	const perPage = 2
	out := make([]domain.WikiChunk, 0, pages*perPage)
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
				Kind:       domain.WikiChunkKindLead,
			})
		}
	}
	return out
}

func producerConfig() ProducerConfig {
	return ProducerConfig{
		Corpus:             "simplewiki",
		DumpVersion:        "Mon, 01 Jun 2026 00:00:00 GMT",
		MaxPriority:        10,
		EnqueueBatchSize:   3,
		DrainPollInterval:  time.Second,
		DrainStallTimeout:  5 * time.Second,
		MaintenanceWorkMem: "64MB",
		MaxParallelWorkers: 0,
	}
}

func decodeJobs(t *testing.T, msgs []publishedMsg) []embedjob.Job {
	t.Helper()
	jobs := make([]embedjob.Job, len(msgs))
	for i, m := range msgs {
		if err := json.Unmarshal(m.body, &jobs[i]); err != nil {
			t.Fatalf("published message %d is not a valid embedjob.Job: %v", i, err)
		}
	}
	return jobs
}

func TestRunBulkEnqueuePublishesOneJobPerChunk(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store := &fakeProducerStore{
			staging:   producerChunks(3),
			remaining: []int64{6, 0}, // start total, then drained on the first drain check
		}
		pub := &fakePublisher{}

		stats, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		if stats.Published != 6 {
			t.Errorf("published = %d, want 6 (one per un-embedded chunk)", stats.Published)
		}

		msgs := pub.published()
		if len(msgs) != 6 {
			t.Fatalf("publisher saw %d messages, want 6", len(msgs))
		}
		// One job per chunk, each carrying the chunk's identity and text and a
		// fresh (zero) attempt. Publishes within a page race, so match by identity
		// rather than position - the broker, not publish order, enforces priority.
		byKey := map[string]embedjob.Job{}
		for _, j := range decodeJobs(t, msgs) {
			byKey[fmt.Sprintf("%d/%d", j.PageID, j.ChunkIndex)] = j
		}
		for _, want := range store.staging {
			key := fmt.Sprintf("%d/%d", want.PageID, want.ChunkIndex)
			got, ok := byKey[key]
			if !ok {
				t.Errorf("no job published for chunk %s", key)
				continue
			}
			if got.Content != want.Content {
				t.Errorf("job %s content = %q, want %q", key, got.Content, want.Content)
			}
			if got.Attempt != 0 {
				t.Errorf("job %s attempt = %d, want 0 (producer enqueues a fresh job)", key, got.Attempt)
			}
		}
		if !store.finalized || store.finalizedCorpus != "simplewiki" {
			t.Errorf("finalized = %v corpus %q, want true simplewiki", store.finalized, store.finalizedCorpus)
		}
		if store.finalizedVersion != producerConfig().DumpVersion {
			t.Errorf("finalized version = %q, want %q", store.finalizedVersion, producerConfig().DumpVersion)
		}
	})
}

func TestRunBulkEnqueuePrioritizesLeadOverBody(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lead := domain.WikiChunk{PageID: 1, ChunkIndex: 0, Corpus: "simplewiki", Content: "lead text", Kind: domain.WikiChunkKindLead}
		body := domain.WikiChunk{PageID: 2, ChunkIndex: 0, Corpus: "simplewiki", Content: "body text", Kind: domain.WikiChunkKindBody}
		store := &fakeProducerStore{staging: []domain.WikiChunk{lead, body}, remaining: []int64{2, 0}}
		pub := &fakePublisher{}

		if _, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig()); err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		msgs := pub.published()
		if len(msgs) != 2 {
			t.Fatalf("published %d messages, want 2", len(msgs))
		}
		// Match priority to the chunk by content, since publishes within a page race.
		// MaxPriority 10: lead maps to the top band, body to half.
		prio := map[string]uint8{}
		for _, m := range msgs {
			var j embedjob.Job
			if err := json.Unmarshal(m.body, &j); err != nil {
				t.Fatalf("decode published job: %v", err)
			}
			prio[j.Content] = m.priority
		}
		if prio["lead text"] != 10 {
			t.Errorf("lead chunk priority = %d, want 10", prio["lead text"])
		}
		if prio["body text"] != 5 {
			t.Errorf("body chunk priority = %d, want 5", prio["body text"])
		}
	})
}

func TestRunBulkEnqueueWaitsForDrainBeforeSwap(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The fleet works the queue down over several polls; the swap must not fire
		// until staging is fully drained.
		store := &fakeProducerStore{staging: producerChunks(2), remaining: []int64{4, 4, 2, 0}}
		pub := &fakePublisher{}

		done := make(chan struct{})
		var stats EnqueueStats
		var runErr error
		go func() {
			stats, runErr = RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
			close(done)
		}()

		// Let the publish loop run and the first drain check observe a positive
		// remaining count; the swap must still be pending.
		synctest.Wait()
		store.mu.Lock()
		finalizedEarly := store.finalized
		store.mu.Unlock()
		if finalizedEarly {
			t.Fatal("staging swapped before the fleet drained the queue")
		}

		<-done
		if runErr != nil {
			t.Fatalf("RunBulkEnqueue: %v", runErr)
		}
		if stats.Published != 4 {
			t.Errorf("published = %d, want 4", stats.Published)
		}
		if !store.finalized {
			t.Error("staging must swap once the fleet has drained")
		}
	})
}

func TestRunBulkEnqueueResumeEnqueuesOnlyRemaining(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// A prior run already embedded page 1 (both chunks), so staging only offers
		// the rest as un-embedded; the producer must enqueue only those.
		staging := producerChunks(3)
		rest := staging[2:] // drop page 1's two chunks
		store := &fakeProducerStore{staging: rest, remaining: []int64{4, 0}}
		pub := &fakePublisher{}

		stats, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err != nil {
			t.Fatalf("RunBulkEnqueue (resume): %v", err)
		}
		if stats.Published != 4 {
			t.Errorf("published = %d, want 4 (resume enqueues only the remainder)", stats.Published)
		}
		pages := map[int64]bool{}
		for _, j := range decodeJobs(t, pub.published()) {
			pages[j.PageID] = true
		}
		if pages[1] {
			t.Error("resume re-enqueued an already-embedded page 1 chunk")
		}
		if !pages[2] || !pages[3] {
			t.Errorf("resume published pages %v, want only the remainder pages 2 and 3", pages)
		}
		if !store.finalized {
			t.Error("resume must still drain and swap")
		}
	})
}

func TestRunBulkEnqueueFullyEmbeddedStillFinalizes(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Nothing left to embed (a prior run died at finalize); the producer
		// publishes nothing but must still swap.
		store := &fakeProducerStore{staging: nil, remaining: []int64{0}}
		pub := &fakePublisher{}

		stats, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		if stats.Published != 0 {
			t.Errorf("published = %d, want 0", stats.Published)
		}
		if len(pub.published()) != 0 {
			t.Errorf("publisher saw %d messages, want 0", len(pub.published()))
		}
		if !store.finalized {
			t.Error("a fully-embedded resume must still finalize and swap")
		}
	})
}

func TestRunBulkEnqueueAbortsOnStalledFleet(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The fleet never makes progress: remaining holds at 4 forever. With a 5s
		// stall timeout and 1s poll, the producer aborts rather than hang, and never
		// swaps a partially-embedded corpus.
		store := &fakeProducerStore{staging: producerChunks(2), remaining: []int64{4}}
		pub := &fakePublisher{}

		_, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err == nil {
			t.Fatal("RunBulkEnqueue returned nil, want a stalled-fleet error")
		}
		if !errors.Is(err, ErrDrainStalled) {
			t.Errorf("error = %v, want ErrDrainStalled", err)
		}
		if store.finalized {
			t.Error("a stalled run must not swap a partially-embedded corpus")
		}
	})
}

func TestRunBulkEnqueueStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// A canceled context (a -max-duration budget or SIGTERM) during the drain
		// wait is a clean resumable stop: it surfaces ctx.Err and never swaps.
		ctx, cancel := context.WithCancel(t.Context())
		store := &fakeProducerStore{staging: producerChunks(2), remaining: []int64{4}}
		pub := &fakePublisher{}

		done := make(chan error, 1)
		go func() {
			_, err := RunBulkEnqueue(ctx, discardLogger(), store, pub, producerConfig())
			done <- err
		}()
		synctest.Wait()
		cancel()

		err := <-done
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if store.finalized {
			t.Error("a canceled run must not swap")
		}
	})
}

func TestPriorityForKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		kind        domain.WikiChunkKind
		maxPriority uint8
		want        uint8
	}{
		{"lead at default ceiling", domain.WikiChunkKindLead, 10, 10},
		{"body at default ceiling", domain.WikiChunkKindBody, 10, 5},
		{"lead at low ceiling", domain.WikiChunkKindLead, 1, 1},
		{"body at low ceiling", domain.WikiChunkKindBody, 1, 0},
		{"unknown kind floors", domain.WikiChunkKind("mystery"), 10, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := priorityForKind(tc.kind, tc.maxPriority); got != tc.want {
				t.Errorf("priorityForKind(%q, %d) = %d, want %d", tc.kind, tc.maxPriority, got, tc.want)
			}
		})
	}
}
