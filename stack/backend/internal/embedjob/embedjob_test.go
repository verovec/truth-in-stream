package embedjob

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// testVec returns a full-dimension embedding whose first element is hot, so a
// store fake can assert which vector it was handed.
func testVec(hot float32) []float32 {
	v := make([]float32, domain.EmbeddingDim)
	v[0] = hot
	return v
}

func mustJob(t *testing.T, j Job) []byte {
	t.Helper()
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	return b
}

// fakeEmbedder returns vec for every input, or err. It counts calls so a test
// can prove a malformed job never reached the provider.
type fakeEmbedder struct {
	vec   []float32
	err   error
	calls atomic.Int32
}

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

type writeRec struct {
	pageID     int64
	chunkIndex int
	embedding  []float32
}

// fakeStore records every write and returns a fixed (updated, err).
type fakeStore struct {
	mu      sync.Mutex
	writes  []writeRec
	updated bool
	err     error
}

func (f *fakeStore) SetStagingChunkEmbedding(_ context.Context, pageID int64, chunkIndex int, embedding []float32) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, writeRec{pageID, chunkIndex, embedding})
	return f.updated, f.err
}

func (f *fakeStore) recorded() []writeRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]writeRec(nil), f.writes...)
}

func newTestWorker(emb Embedder, st Store, cfg Config) *Worker {
	return NewWorker(emb, st, nil, nil, slog.New(slog.DiscardHandler), cfg)
}

func TestProcessEmbedsAndWrites(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: testVec(7)}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{PageID: 42, ChunkIndex: 1, Content: "hello"}), 5)

	if got.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck", got.Action)
	}
	writes := st.recorded()
	if len(writes) != 1 {
		t.Fatalf("store writes = %d, want 1", len(writes))
	}
	if writes[0].pageID != 42 || writes[0].chunkIndex != 1 || writes[0].embedding[0] != 7 {
		t.Fatalf("write = %+v, want page 42 chunk 1 vec[0]=7", writes[0])
	}
	if emb.calls.Load() != 1 {
		t.Fatalf("embed calls = %d, want 1", emb.calls.Load())
	}
}

func TestProcessMalformedBodyDropsWithoutEmbedding(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), []byte("{not json"), 5)

	if got.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck (drop poison)", got.Action)
	}
	if emb.calls.Load() != 0 {
		t.Fatalf("embed calls = %d, want 0 (malformed job must not reach the provider)", emb.calls.Load())
	}
	if len(st.recorded()) != 0 {
		t.Fatalf("store writes = %d, want 0", len(st.recorded()))
	}
}

func TestProcessInvalidJobDropsWithoutEmbedding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		job  Job
	}{
		{name: "zero page id", job: Job{PageID: 0, ChunkIndex: 0, Content: "x"}},
		{name: "negative chunk index", job: Job{PageID: 1, ChunkIndex: -1, Content: "x"}},
		{name: "empty content", job: Job{PageID: 1, ChunkIndex: 0, Content: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			emb := &fakeEmbedder{vec: testVec(1)}
			st := &fakeStore{updated: true}
			w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

			got := w.Process(t.Context(), mustJob(t, tc.job), 5)

			if got.Action != ActionAck {
				t.Fatalf("action = %v, want ActionAck", got.Action)
			}
			if emb.calls.Load() != 0 {
				t.Fatalf("embed calls = %d, want 0", emb.calls.Load())
			}
		})
	}
}

func TestProcessObsoleteChunkDrops(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: false} // no staging row matches
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "gone"}), 5)

	if got.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck (drop obsolete job)", got.Action)
	}
}

func TestProcessTransientEmbedFailureRepublishes(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{err: errors.New("voyage down")}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x", Attempt: 0}), 9)

	if got.Action != ActionRepublish {
		t.Fatalf("action = %v, want ActionRepublish", got.Action)
	}
	if got.RepublishPriority != 9 {
		t.Fatalf("republish priority = %d, want 9 (preserved)", got.RepublishPriority)
	}
	var requeued Job
	if err := json.Unmarshal(got.RepublishBody, &requeued); err != nil {
		t.Fatalf("republish body not a job: %v", err)
	}
	if requeued.Attempt != 1 {
		t.Fatalf("republished attempt = %d, want 1", requeued.Attempt)
	}
	if requeued.PageID != 1 || requeued.Content != "x" {
		t.Fatalf("republished job = %+v, want same identity/content", requeued)
	}
}

func TestProcessTransientWriteFailureRepublishes(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: false, err: errors.New("db down")}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x"}), 4)

	if got.Action != ActionRepublish {
		t.Fatalf("action = %v, want ActionRepublish on write failure", got.Action)
	}
}

func TestProcessExhaustedRetriesDrops(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{err: errors.New("still down")}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	// Attempt 2 is the third (final) try for MaxAttempts=3.
	got := w.Process(t.Context(), mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x", Attempt: 2}), 5)

	if got.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck (drop after exhausting attempts)", got.Action)
	}
}

func TestProcessContextCanceledRequeues(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	emb := &fakeEmbedder{err: context.Canceled}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(ctx, mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x"}), 5)

	if got.Action != ActionRequeue {
		t.Fatalf("action = %v, want ActionRequeue (shutdown must not drop or burn an attempt)", got.Action)
	}
}

func TestProcessWrongEmbeddingShapeDrops(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: []float32{1, 2, 3}} // wrong dimension
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x"}), 5)

	if got.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck (a deterministic bad shape must not loop)", got.Action)
	}
	if len(st.recorded()) != 0 {
		t.Fatalf("store writes = %d, want 0 (never write a malformed vector)", len(st.recorded()))
	}
}

func TestProcessRedeliveryIsIdempotent(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: testVec(3)}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})
	body := mustJob(t, Job{PageID: 5, ChunkIndex: 2, Content: "dup"})

	first := w.Process(t.Context(), body, 5)
	second := w.Process(t.Context(), body, 5)

	if first.Action != ActionAck || second.Action != ActionAck {
		t.Fatalf("actions = %v, %v; want both ActionAck", first.Action, second.Action)
	}
	writes := st.recorded()
	if len(writes) != 2 {
		t.Fatalf("store writes = %d, want 2 (each delivery writes the same vector safely)", len(writes))
	}
	for _, wr := range writes {
		if wr.pageID != 5 || wr.chunkIndex != 2 || wr.embedding[0] != 3 {
			t.Fatalf("write = %+v, want page 5 chunk 2 vec[0]=3", wr)
		}
	}
}

// --- Run loop (routing + concurrency) ---

// recDelivery records the acknowledgement the loop applied.
type recDelivery struct {
	body     []byte
	priority uint8
	mu       sync.Mutex
	acked    bool
	nacked   bool
	requeue  bool
}

func (d *recDelivery) Body() []byte    { return d.body }
func (d *recDelivery) Priority() uint8 { return d.priority }
func (d *recDelivery) Ack() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.acked = true
	return nil
}

func (d *recDelivery) Nack(requeue bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nacked = true
	d.requeue = requeue
	return nil
}

func (d *recDelivery) state() (acked, nacked, requeue bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.acked, d.nacked, d.requeue
}

// sliceStream yields a fixed set of deliveries once, then closes.
type sliceStream struct {
	deliveries []Delivery
}

func (s *sliceStream) Consume(_ context.Context) (<-chan Delivery, error) {
	out := make(chan Delivery)
	go func() {
		defer close(out)
		for _, d := range s.deliveries {
			out <- d
		}
	}()
	return out, nil
}

type recEnqueuer struct {
	mu       sync.Mutex
	bodies   [][]byte
	err      error
	priority []uint8
}

func (e *recEnqueuer) Enqueue(_ context.Context, body []byte, priority uint8) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	e.bodies = append(e.bodies, body)
	e.priority = append(e.priority, priority)
	return nil
}

func (e *recEnqueuer) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.bodies)
}

func TestRunAcksSuccessfulDeliveries(t *testing.T) {
	t.Parallel()
	d1 := &recDelivery{body: mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "a"}), priority: 5}
	d2 := &recDelivery{body: mustJob(t, Job{PageID: 2, ChunkIndex: 0, Content: "b"}), priority: 5}
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: true}
	enq := &recEnqueuer{}
	w := NewWorker(emb, st, &sliceStream{[]Delivery{d1, d2}}, enq, slog.New(slog.DiscardHandler), Config{Concurrency: 2, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i, d := range []*recDelivery{d1, d2} {
		acked, nacked, _ := d.state()
		if !acked || nacked {
			t.Fatalf("delivery %d acked=%v nacked=%v, want acked", i, acked, nacked)
		}
	}
	if enq.count() != 0 {
		t.Fatalf("enqueue count = %d, want 0", enq.count())
	}
}

func TestRunRepublishesThenAcksOriginal(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x"}), priority: 8}
	emb := &fakeEmbedder{err: errors.New("transient")}
	st := &fakeStore{updated: true}
	enq := &recEnqueuer{}
	w := NewWorker(emb, st, &sliceStream{[]Delivery{d}}, enq, slog.New(slog.DiscardHandler), Config{Concurrency: 1, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if enq.count() != 1 {
		t.Fatalf("enqueue count = %d, want 1 (transient failure re-enqueues)", enq.count())
	}
	acked, nacked, _ := d.state()
	if !acked || nacked {
		t.Fatalf("original acked=%v nacked=%v, want acked after a successful republish", acked, nacked)
	}
}

func TestRunRequeuesOriginalWhenRepublishFails(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x"}), priority: 8}
	emb := &fakeEmbedder{err: errors.New("transient")}
	st := &fakeStore{updated: true}
	enq := &recEnqueuer{err: errors.New("broker down")}
	w := NewWorker(emb, st, &sliceStream{[]Delivery{d}}, enq, slog.New(slog.DiscardHandler), Config{Concurrency: 1, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, nacked, requeue := d.state()
	if acked {
		t.Fatalf("original was acked despite a failed republish; the job would be lost")
	}
	if !nacked || !requeue {
		t.Fatalf("original nacked=%v requeue=%v, want nacked with requeue=true", nacked, requeue)
	}
}

func TestRunNacksRequeueOnShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d := &recDelivery{body: mustJob(t, Job{PageID: 1, ChunkIndex: 0, Content: "x"}), priority: 5}
	emb := &fakeEmbedder{err: context.Canceled}
	st := &fakeStore{updated: true}
	enq := &recEnqueuer{}
	w := NewWorker(emb, st, &sliceStream{[]Delivery{d}}, enq, slog.New(slog.DiscardHandler), Config{Concurrency: 1, MaxAttempts: 3})

	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, nacked, requeue := d.state()
	if acked {
		t.Fatalf("delivery acked during shutdown; in-flight work would be lost")
	}
	if !nacked || !requeue {
		t.Fatalf("delivery nacked=%v requeue=%v, want requeue on shutdown", nacked, requeue)
	}
}

func TestRunBoundsConcurrency(t *testing.T) {
	t.Parallel()
	const limit = 3
	const jobs = 12
	var live atomic.Int32
	var peak atomic.Int32
	gate := make(chan struct{})
	entered := make(chan struct{}, jobs)
	emb := &blockingEmbedder{vec: testVec(1), live: &live, peak: &peak, release: gate, entered: entered}
	st := &fakeStore{updated: true}

	deliveries := make([]Delivery, jobs)
	for i := range deliveries {
		deliveries[i] = &recDelivery{body: mustJob(t, Job{PageID: int64(i + 1), ChunkIndex: 0, Content: "x"}), priority: 5}
	}
	w := NewWorker(emb, st, &sliceStream{deliveries}, &recEnqueuer{}, slog.New(slog.DiscardHandler), Config{Concurrency: limit, MaxAttempts: 3})

	done := make(chan error, 1)
	go func() { done <- w.Run(t.Context()) }()

	// The loop admits exactly `limit` handlers; they block in the embedder and
	// signal entry. A fourth signal would mean the semaphore was breached.
	for range limit {
		<-entered
	}
	select {
	case <-entered:
		t.Fatalf("more than %d handlers ran concurrently", limit)
	default:
	}
	close(gate) // release the in-flight handlers; the rest follow as slots free
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := peak.Load(); got > limit {
		t.Fatalf("peak concurrency = %d, want <= %d", got, limit)
	}
	for i, d := range deliveries {
		if acked, _, _ := d.(*recDelivery).state(); !acked {
			t.Fatalf("delivery %d not acked", i)
		}
	}
}

// blockingEmbedder records peak concurrency, signals each entry, then blocks on
// release, so a test can observe the concurrency the loop allows.
type blockingEmbedder struct {
	vec     []float32
	live    *atomic.Int32
	peak    *atomic.Int32
	release <-chan struct{}
	entered chan<- struct{}
}

func (b *blockingEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	n := b.live.Add(1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	b.entered <- struct{}{}
	<-b.release
	b.live.Add(-1)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = b.vec
	}
	return out, nil
}
