package embedjob

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// testSource is the evidence source every job fixture is stamped with. A chunk is
// keyed on (source, external_id, chunk_index), so a valid job needs a non-empty
// source and external id; the fixtures carry the old page id as the external id.
const testSource = "wiki"

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
	source     string
	externalID string
	chunkIndex int
	embedding  []float32
	staging    bool
}

// fakeStore records every write. updated controls whether the batch reports its
// rows matched (the normal case) or zero (an obsolete job); err forces a write
// failure. Both batch methods funnel through record so a test can assert which
// corpus a job was routed to via the staging flag on each writeRec.
type fakeStore struct {
	mu      sync.Mutex
	writes  []writeRec
	updated bool
	err     error
}

func (f *fakeStore) SetLiveChunkEmbeddings(_ context.Context, chunks []domain.EvidenceChunk) (int, error) {
	return f.record(chunks, false)
}

func (f *fakeStore) SetStagingChunkEmbeddings(_ context.Context, chunks []domain.EvidenceChunk) (int, error) {
	return f.record(chunks, true)
}

func (f *fakeStore) record(chunks []domain.EvidenceChunk, staging bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range chunks {
		f.writes = append(f.writes, writeRec{c.Source, c.ExternalID, c.ChunkIndex, c.Embedding, staging})
	}
	if f.err != nil {
		return 0, f.err
	}
	if !f.updated {
		return 0, nil
	}
	return len(chunks), nil
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

	got := w.Process(t.Context(), mustJob(t, Job{Source: testSource, ExternalID: "42", ChunkIndex: 1, Content: "hello"}), 5)

	if got.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck", got.Action)
	}
	writes := st.recorded()
	if len(writes) != 1 {
		t.Fatalf("store writes = %d, want 1", len(writes))
	}
	if writes[0].externalID != "42" || writes[0].chunkIndex != 1 || writes[0].embedding[0] != 7 {
		t.Fatalf("write = %+v, want external 42 chunk 1 vec[0]=7", writes[0])
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

	if got.Action != ActionReject {
		t.Fatalf("action = %v, want ActionReject (dead-letter poison)", got.Action)
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
		{name: "empty source", job: Job{ExternalID: "1", ChunkIndex: 0, Content: "x"}},
		{name: "empty external id", job: Job{Source: testSource, ChunkIndex: 0, Content: "x"}},
		{name: "negative chunk index", job: Job{Source: testSource, ExternalID: "1", ChunkIndex: -1, Content: "x"}},
		{name: "empty content", job: Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: ""}},
		{name: "negative attempt", job: Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x", Attempt: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			emb := &fakeEmbedder{vec: testVec(1)}
			st := &fakeStore{updated: true}
			w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

			got := w.Process(t.Context(), mustJob(t, tc.job), 5)

			if got.Action != ActionReject {
				t.Fatalf("action = %v, want ActionReject (dead-letter invalid)", got.Action)
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

	got := w.Process(t.Context(), mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "gone"}), 5)

	if got.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck (drop obsolete job)", got.Action)
	}
}

func TestProcessTransientEmbedFailureRepublishes(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{err: errors.New("voyage down")}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x", Attempt: 0}), 9)

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
	if requeued.ExternalID != "1" || requeued.Content != "x" {
		t.Fatalf("republished job = %+v, want same identity/content", requeued)
	}
}

func TestProcessTransientWriteFailureRepublishes(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: false, err: errors.New("db down")}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x"}), 4)

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
	got := w.Process(t.Context(), mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x", Attempt: 2}), 5)

	if got.Action != ActionReject {
		t.Fatalf("action = %v, want ActionReject (dead-letter after exhausting attempts)", got.Action)
	}
}

func TestProcessHugeAttemptDropsWithoutLooping(t *testing.T) {
	t.Parallel()
	// A crafted job whose attempt is near the integer ceiling must be dropped,
	// not re-enqueued: computing attempt+1 would overflow negative and dodge the
	// cap, looping forever.
	emb := &fakeEmbedder{err: errors.New("down")}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 5})

	got := w.Process(t.Context(), mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x", Attempt: math.MaxInt}), 5)

	if got.Action != ActionReject {
		t.Fatalf("action = %v, want ActionReject (a near-max attempt must dead-letter, not loop)", got.Action)
	}
}

func TestProcessContextCanceledRequeues(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	emb := &fakeEmbedder{err: context.Canceled}
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(ctx, mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x"}), 5)

	if got.Action != ActionRequeue {
		t.Fatalf("action = %v, want ActionRequeue (shutdown must not drop or burn an attempt)", got.Action)
	}
}

func TestProcessWrongEmbeddingShapeDrops(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vec: []float32{1, 2, 3}} // wrong dimension
	st := &fakeStore{updated: true}
	w := newTestWorker(emb, st, Config{Concurrency: 1, MaxAttempts: 3})

	got := w.Process(t.Context(), mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x"}), 5)

	if got.Action != ActionReject {
		t.Fatalf("action = %v, want ActionReject (a deterministic bad shape must dead-letter, not loop)", got.Action)
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
	body := mustJob(t, Job{Source: testSource, ExternalID: "5", ChunkIndex: 2, Content: "dup"})

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
		if wr.externalID != "5" || wr.chunkIndex != 2 || wr.embedding[0] != 3 {
			t.Fatalf("write = %+v, want external 5 chunk 2 vec[0]=3", wr)
		}
	}
}

// --- Run loop (routing + concurrency) ---

// recDelivery records the acknowledgement the loop applied.
type recDelivery struct {
	body     []byte
	priority uint8
	version  string
	mu       sync.Mutex
	acked    bool
	nacked   bool
	requeue  bool
}

func (d *recDelivery) Body() []byte    { return d.body }
func (d *recDelivery) Priority() uint8 { return d.priority }
func (d *recDelivery) Version() string { return d.version }
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

// TestRunDropsUnknownVersionWithoutEmbedding proves the version guard: a worker
// configured to know only version "1" drops a delivery stamped "2" (acks it, no
// nack) without ever calling the embedder, so a stray message from another queue
// version is parked rather than mis-processed; a known version still flows.
func TestRunDropsUnknownVersionWithoutEmbedding(t *testing.T) {
	t.Parallel()
	unknown := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "stray"}), priority: 5, version: "2"}
	known := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "2", ChunkIndex: 0, Content: "ok"}), priority: 5, version: "1"}
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: true}
	w := NewWorker(emb, st, &sliceStream{[]Delivery{unknown, known}}, &recEnqueuer{}, slog.New(slog.DiscardHandler),
		Config{Concurrency: 1, MaxAttempts: 3, KnownVersions: []string{"1"}})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if acked, nacked, requeue := unknown.state(); acked || !nacked || requeue {
		t.Fatalf("unknown-version delivery acked=%v nacked=%v requeue=%v, want dead-lettered (nacked, requeue=false)", acked, nacked, requeue)
	}
	if acked, nacked, _ := known.state(); !acked || nacked {
		t.Fatalf("known-version delivery acked=%v nacked=%v, want processed (acked)", acked, nacked)
	}
	// Only the known-version job reached the embedder.
	if got := emb.calls.Load(); got != 1 {
		t.Fatalf("embedder calls = %d, want 1 (the unknown version must not embed)", got)
	}
}

func TestRunAcksSuccessfulDeliveries(t *testing.T) {
	t.Parallel()
	d1 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "a"}), priority: 5}
	d2 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "2", ChunkIndex: 0, Content: "b"}), priority: 5}
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
	d := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x"}), priority: 8}
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

func TestRunDeadLettersOriginalWhenRepublishFails(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x"}), priority: 8}
	emb := &fakeEmbedder{err: errors.New("transient")}
	st := &fakeStore{updated: true}
	enq := &recEnqueuer{err: errors.New("broker down")} // non-shutdown re-enqueue failure
	w := NewWorker(emb, st, &sliceStream{[]Delivery{d}}, enq, slog.New(slog.DiscardHandler), Config{Concurrency: 1, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, nacked, requeue := d.state()
	if acked {
		t.Fatalf("original was acked despite a failed republish; the job would be lost")
	}
	// The fix for the infinite-requeue bug: a failed re-enqueue dead-letters the
	// original (requeue=false) instead of looping it forever with an unadvanced attempt.
	if !nacked || requeue {
		t.Fatalf("original nacked=%v requeue=%v, want dead-lettered (nacked, requeue=false)", nacked, requeue)
	}
}

func TestRunNacksRequeueOnShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "x"}), priority: 5}
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
		deliveries[i] = &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: strconv.Itoa(i + 1), ChunkIndex: 0, Content: "x"}), priority: 5}
	}
	// BatchSize 1 makes each delivery its own batch, so the test observes the
	// loop's concurrency over batches directly.
	w := NewWorker(emb, st, &sliceStream{deliveries}, &recEnqueuer{}, slog.New(slog.DiscardHandler), Config{Concurrency: limit, BatchSize: 1, MaxAttempts: 3})

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

// --- Batching ---

func recDeliveries(t *testing.T, n int, staging bool) []Delivery {
	t.Helper()
	out := make([]Delivery, n)
	for i := range out {
		out[i] = &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: strconv.Itoa(i + 1), ChunkIndex: 0, Content: "c", Staging: staging}), priority: 5}
	}
	return out
}

func assertAllAcked(t *testing.T, deliveries []Delivery) {
	t.Helper()
	for i, d := range deliveries {
		acked, nacked, _ := d.(*recDelivery).state()
		if !acked || nacked {
			t.Fatalf("delivery %d acked=%v nacked=%v, want acked", i, acked, nacked)
		}
	}
}

// TestRunEmbedsWholeBatchInOneCall is the throughput property: a batch of N
// chunks costs one provider round-trip, not N.
func TestRunEmbedsWholeBatchInOneCall(t *testing.T) {
	t.Parallel()
	const n = 5
	deliveries := recDeliveries(t, n, false)
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: true}
	// A long BatchWait makes the stream close, not the timer, trigger the flush,
	// so the assertion on a single call is deterministic.
	w := NewWorker(emb, st, &sliceStream{deliveries}, &recEnqueuer{}, slog.New(slog.DiscardHandler),
		Config{Concurrency: 2, BatchSize: 16, BatchWait: time.Minute, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := emb.calls.Load(); got != 1 {
		t.Fatalf("embed calls = %d, want 1 (the whole batch embeds in one call)", got)
	}
	if got := len(st.recorded()); got != n {
		t.Fatalf("writes = %d, want %d", got, n)
	}
	assertAllAcked(t, deliveries)
}

// TestRunRoutesStagingFlagToCorpus proves the worker writes a default job into
// the live corpus and a Staging job into staging, so the fleet serves both the
// bulk-into-live default and an atomic rebuild.
func TestRunRoutesStagingFlagToCorpus(t *testing.T) {
	t.Parallel()
	live := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "live"}), priority: 5}
	staged := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "2", ChunkIndex: 0, Content: "stage", Staging: true}), priority: 5}
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: true}
	w := NewWorker(emb, st, &sliceStream{[]Delivery{live, staged}}, &recEnqueuer{}, slog.New(slog.DiscardHandler),
		Config{Concurrency: 1, BatchSize: 16, BatchWait: time.Minute, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	writes := st.recorded()
	if len(writes) != 2 {
		t.Fatalf("writes = %d, want 2", len(writes))
	}
	for _, wr := range writes {
		switch wr.externalID {
		case "1":
			if wr.staging {
				t.Errorf("external 1 routed to staging, want live")
			}
		case "2":
			if !wr.staging {
				t.Errorf("external 2 routed to live, want staging")
			}
		}
	}
}

// batchFailEmbedder fails any multi-input call (mimicking a size-class provider
// rejection) but succeeds on single inputs, so a test can prove the batch is
// recovered by splitting down to inputs the provider accepts.
type batchFailEmbedder struct {
	vec        []float32
	batchCalls atomic.Int32
}

func (e *batchFailEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) > 1 {
		e.batchCalls.Add(1)
		return nil, errors.New("batch endpoint hung")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = e.vec
	}
	return out, nil
}

// TestRunBatchEmbedErrorSplitsAndRecovers proves a batch-level embed failure is
// recovered by recursively halving the batch until each sub-batch embeds, rather
// than failing the whole batch or dead-lettering any chunk.
func TestRunBatchEmbedErrorSplitsAndRecovers(t *testing.T) {
	t.Parallel()
	const n = 4
	deliveries := recDeliveries(t, n, false)
	emb := &batchFailEmbedder{vec: testVec(1)}
	st := &fakeStore{updated: true}
	enq := &recEnqueuer{}
	w := NewWorker(emb, st, &sliceStream{deliveries}, enq, slog.New(slog.DiscardHandler),
		Config{Concurrency: 1, BatchSize: 16, BatchWait: time.Minute, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The worker attempted at least one multi-input call (which failed) before
	// splitting, proving it did not skip straight to one call per chunk.
	if got := emb.batchCalls.Load(); got < 1 {
		t.Fatalf("batch calls = %d, want at least one whole-batch attempt before splitting", got)
	}
	if got := len(st.recorded()); got != n {
		t.Fatalf("writes = %d, want %d (every chunk embedded after the split)", got, n)
	}
	if enq.count() != 0 {
		t.Fatalf("enqueue count = %d, want 0 (the split recovered, no retries)", enq.count())
	}
	assertAllAcked(t, deliveries)
}

// shapeEmbedder returns a wrong-dimension vector for one input and a good one
// for the rest, so a test can prove one poison shape is dropped without sinking
// its batch-mates.
type shapeEmbedder struct {
	vec        []float32
	badContent string
}

func (e *shapeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		if txt == e.badContent {
			out[i] = []float32{1, 2, 3}
			continue
		}
		out[i] = e.vec
	}
	return out, nil
}

func TestRunBatchDeadLettersBadShapeKeepsRest(t *testing.T) {
	t.Parallel()
	good1 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "g1"}), priority: 5}
	bad := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "2", ChunkIndex: 0, Content: "bad"}), priority: 5}
	good2 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "3", ChunkIndex: 0, Content: "g2"}), priority: 5}
	emb := &shapeEmbedder{vec: testVec(1), badContent: "bad"}
	st := &fakeStore{updated: true}
	w := NewWorker(emb, st, &sliceStream{[]Delivery{good1, bad, good2}}, &recEnqueuer{}, slog.New(slog.DiscardHandler),
		Config{Concurrency: 1, BatchSize: 16, BatchWait: time.Minute, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	writes := st.recorded()
	if len(writes) != 2 {
		t.Fatalf("writes = %d, want 2 (the bad-shape chunk is never written)", len(writes))
	}
	for _, wr := range writes {
		if wr.externalID == "2" {
			t.Errorf("bad-shape chunk external 2 was written")
		}
	}
	// The two good deliveries are acked; the bad-shape one is dead-lettered, not
	// acked away, so it is inspectable in the DLQ.
	assertAllAcked(t, []Delivery{good1, good2})
	if acked, nacked, requeue := bad.state(); acked || !nacked || requeue {
		t.Fatalf("bad-shape delivery acked=%v nacked=%v requeue=%v, want dead-lettered (nacked, requeue=false)", acked, nacked, requeue)
	}
}

func TestRunBatchWriteFailureRepublishesJobs(t *testing.T) {
	t.Parallel()
	const n = 3
	deliveries := recDeliveries(t, n, false)
	emb := &fakeEmbedder{vec: testVec(1)}
	st := &fakeStore{err: errors.New("db down")}
	enq := &recEnqueuer{}
	w := NewWorker(emb, st, &sliceStream{deliveries}, enq, slog.New(slog.DiscardHandler),
		Config{Concurrency: 1, BatchSize: 16, BatchWait: time.Minute, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if enq.count() != n {
		t.Fatalf("enqueue count = %d, want %d (a write failure re-enqueues each job)", enq.count(), n)
	}
	assertAllAcked(t, deliveries)
}

// TestRunBatchWaitFlushesPartialBatch proves the max-wait window: a batch that
// never fills is embedded once the window elapses, so a quiet queue still drains.
func TestRunBatchWaitFlushesPartialBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d1 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "1", ChunkIndex: 0, Content: "a"}), priority: 5}
		d2 := &recDelivery{body: mustJob(t, Job{Source: testSource, ExternalID: "2", ChunkIndex: 0, Content: "b"}), priority: 5}
		emb := &fakeEmbedder{vec: testVec(1)}
		st := &fakeStore{updated: true}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		w := NewWorker(emb, st, &openStream{[]Delivery{d1, d2}}, &recEnqueuer{}, slog.New(slog.DiscardHandler),
			Config{Concurrency: 1, BatchSize: 16, BatchWait: 200 * time.Millisecond, MaxAttempts: 3})

		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()

		// Both deliveries are collected but the batch is not full; advancing the
		// fake clock past the wait window fires the timer, and Wait then lets the
		// flush and embed complete before the assertion.
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		if got := emb.calls.Load(); got != 1 {
			t.Fatalf("embed calls = %d, want 1 (partial batch flushed on the wait window)", got)
		}
		assertAllAcked(t, []Delivery{d1, d2})

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run: %v", err)
		}
	})
}

// openStream emits its deliveries then stays open until ctx is canceled, so a
// test can exercise the wait-window flush on a stream that has not closed.
type openStream struct {
	deliveries []Delivery
}

func (s *openStream) Consume(ctx context.Context) (<-chan Delivery, error) {
	out := make(chan Delivery)
	go func() {
		defer close(out)
		for _, d := range s.deliveries {
			select {
			case out <- d:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return out, nil
}
