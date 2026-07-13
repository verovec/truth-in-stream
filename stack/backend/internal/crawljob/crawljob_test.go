package crawljob

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeEmbedder struct {
	vec [][]float32
	err error
}

func (f fakeEmbedder) EmbedDocuments(_ context.Context, _ []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

type fakeStore struct {
	got domain.EvidenceChunk
	err error
}

func (f *fakeStore) UpsertEmbeddedChunk(_ context.Context, c domain.EvidenceChunk) error {
	f.got = c
	return f.err
}

// chunkKey is the generalized evidence natural key (source, external_id,
// chunk_index) that replaced the wiki-shaped (page_id, chunk_index) pair.
type chunkKey struct {
	source     string
	externalID string
	chunkIndex int
}

func fullVec() []float32 { return make([]float32, domain.EmbeddingDim) }

func validJob() CrawlJob {
	return CrawlJob{
		PageID: 5, ChunkIndex: 1, Title: "Atom", URL: "u", RevisionID: 9,
		Corpus: "simplewiki-crawl", Content: "Atom\n\ntext", Section: "", Kind: "body",
	}
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
	res := w.Process(t.Context(), mustBody(t, validJob()), 5)
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want Ack", res.Action)
	}
	if st.got.ExternalID != "5" || st.got.Kind != domain.EvidenceKindBody || len(st.got.Embedding) != domain.EmbeddingDim {
		t.Errorf("upserted chunk wrong: %+v", st.got)
	}
	meta, err := domain.ParseWikiMetadata(st.got.Metadata)
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	if st.got.Title != "Atom" || st.got.URL != "u" || meta.RevisionID != 9 || st.got.Source != "simplewiki-crawl" {
		t.Errorf("upserted chunk metadata wrong: %+v", st.got)
	}
}

func TestProcessMalformedIsDeadLettered(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	if res := w.Process(t.Context(), []byte("{not json"), 0); res.Action != ActionReject {
		t.Errorf("action = %v, want Reject (dead-letter a poison message)", res.Action)
	}
}

func TestProcessInvalidJobIsDeadLettered(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	bad := validJob()
	bad.Content = ""
	if res := w.Process(t.Context(), mustBody(t, bad), 0); res.Action != ActionReject {
		t.Errorf("action = %v, want Reject (dead-letter an invalid job)", res.Action)
	}
}

func TestProcessWrongDimIsDeadLettered(t *testing.T) {
	w := NewWorker(fakeEmbedder{vec: [][]float32{{0.1, 0.2}}}, &fakeStore{}, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(t.Context(), mustBody(t, validJob()), 0); res.Action != ActionReject {
		t.Errorf("action = %v, want Reject (dead-letter a provider-contract violation)", res.Action)
	}
}

func TestProcessTransientFailureRepublishes(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 3})
	res := w.Process(t.Context(), mustBody(t, validJob()), 7)
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

func TestProcessShutdownRequeues(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(ctx, mustBody(t, validJob()), 0); res.Action != ActionRequeue {
		t.Errorf("action = %v, want Requeue on shutdown", res.Action)
	}
}

func TestProcessExhaustedAttemptsDeadLettered(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 2})
	j := validJob()
	j.Attempt = 1 // already at budget-1
	if res := w.Process(t.Context(), mustBody(t, j), 0); res.Action != ActionReject {
		t.Errorf("action = %v, want Reject (dead-letter after exhausting retries)", res.Action)
	}
}

func TestProcessEmbedErrorRepublishes(t *testing.T) {
	w := NewWorker(fakeEmbedder{err: errors.New("voyage 429")}, &fakeStore{}, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(t.Context(), mustBody(t, validJob()), 4); res.Action != ActionRepublish {
		t.Errorf("action = %v, want Republish on transient embed error", res.Action)
	}
}

// TestProcessRedeliveryIsIdempotent proves an at-least-once redelivery upserts the
// same chunk key both times: the worker performs no duplicate-suppression, so
// safety rests on the store's UpsertEmbeddedChunk being an idempotent upsert on
// (source, external_id, chunk_index) (proven by store.TestUpsertEmbeddedChunkIsIdempotent).
func TestProcessRedeliveryIsIdempotent(t *testing.T) {
	st := &fakeStore{}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
	body := mustBody(t, validJob())

	first := w.Process(t.Context(), body, 5)
	firstKey := chunkKey{st.got.Source, st.got.ExternalID, st.got.ChunkIndex}
	second := w.Process(t.Context(), body, 5)
	secondKey := chunkKey{st.got.Source, st.got.ExternalID, st.got.ChunkIndex}

	if first.Action != ActionAck || second.Action != ActionAck {
		t.Fatalf("actions = %v, %v; want both ActionAck", first.Action, second.Action)
	}
	if firstKey != secondKey || firstKey != (chunkKey{"simplewiki-crawl", "5", 1}) {
		t.Fatalf("redelivery upserted keys %v then %v, want both (source simplewiki-crawl, external 5, chunk 1)", firstKey, secondKey)
	}
}

// --- Run/handle loop: ack-after-write and shutdown requeue ---

// recDelivery records which acknowledgement the loop applied to it.
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
	d.nacked, d.requeue = true, requeue
	return nil
}

func (d *recDelivery) state() (acked, nacked, requeue bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.acked, d.nacked, d.requeue
}

// sliceStream yields a fixed set of deliveries once, then closes, mirroring the
// broker stream closing on ctx cancellation so Run terminates.
type sliceStream struct{ deliveries []Delivery }

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

// TestRunAcksAfterUpsertAndReturns proves the ack-after-write ordering end to end:
// Run drains a delivery whose upsert commits, acks it (never nacks), and returns
// once the stream closes - a bounded exit with no leaked delivery.
func TestRunAcksAfterUpsertAndReturns(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: mustBody(t, validJob()), priority: 5}
	st := &fakeStore{}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, &sliceStream{[]Delivery{d}}, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, nacked, _ := d.state()
	if !acked || nacked {
		t.Fatalf("delivery acked=%v nacked=%v, want acked after a committed upsert", acked, nacked)
	}
	if st.got.ExternalID != "5" {
		t.Fatalf("upsert did not run before ack: stored external id = %q", st.got.ExternalID)
	}
}

// TestHandleNacksRequeueOnShutdown proves an in-flight delivery interrupted by a
// shutdown (canceled context, so embed fails) is nacked WITH requeue and never
// acked, so the broker redelivers it without the worker burning an attempt.
func TestHandleNacksRequeueOnShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d := &recDelivery{body: mustBody(t, validJob()), priority: 5}
	w := NewWorker(fakeEmbedder{err: context.Canceled}, &fakeStore{}, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	w.handle(ctx, d)

	acked, nacked, requeue := d.state()
	if acked {
		t.Fatal("delivery acked during shutdown; interrupted work would be lost")
	}
	if !nacked || !requeue {
		t.Fatalf("delivery nacked=%v requeue=%v, want nacked with requeue=true", nacked, requeue)
	}
}

// fakeEnqueuer stands in for the re-enqueue publisher; err drives the
// re-enqueue-failure paths.
type fakeEnqueuer struct{ err error }

func (f fakeEnqueuer) Enqueue(_ context.Context, _ []byte, _ uint8) error { return f.err }

// cancelingEnqueuer cancels the context as its Enqueue fails, standing in for a
// shutdown that races an in-flight re-enqueue.
type cancelingEnqueuer struct{ cancel context.CancelFunc }

func (e cancelingEnqueuer) Enqueue(_ context.Context, _ []byte, _ uint8) error {
	e.cancel()
	return context.Canceled
}

// TestHandleDeadLettersUnknownVersion proves a delivery stamped with a version the
// worker does not understand is nacked WITHOUT requeue (dead-lettered), never
// acked away.
func TestHandleDeadLettersUnknownVersion(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: mustBody(t, validJob()), priority: 5, version: "999"}
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3, KnownVersions: []string{"1"}})

	w.handle(t.Context(), d)

	acked, nacked, requeue := d.state()
	if acked || !nacked || requeue {
		t.Fatalf("delivery acked=%v nacked=%v requeue=%v, want dead-lettered (nacked, requeue=false)", acked, nacked, requeue)
	}
}

// TestHandleDeadLettersExhaustedJob proves a job past its retry budget is
// dead-lettered by the dispatch loop, not acked away.
func TestHandleDeadLettersExhaustedJob(t *testing.T) {
	t.Parallel()
	j := validJob()
	j.Attempt = 1
	d := &recDelivery{body: mustBody(t, j), priority: 5}
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 2})

	w.handle(t.Context(), d)

	acked, nacked, requeue := d.state()
	if acked || !nacked || requeue {
		t.Fatalf("delivery acked=%v nacked=%v requeue=%v, want dead-lettered (nacked, requeue=false)", acked, nacked, requeue)
	}
}

// TestHandleDeadLettersWhenRepublishFails proves the infinite-loop fix: when the
// re-enqueue itself fails for a non-shutdown reason the original is dead-lettered,
// not requeued forever with an unadvanced attempt.
func TestHandleDeadLettersWhenRepublishFails(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: mustBody(t, validJob()), priority: 5}
	st := &fakeStore{err: errors.New("db down")} // transient, attempts remain
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, fakeEnqueuer{err: errors.New("broker down")}, nil, Config{Concurrency: 1, MaxAttempts: 3})

	w.handle(t.Context(), d)

	acked, nacked, requeue := d.state()
	if acked || !nacked || requeue {
		t.Fatalf("delivery acked=%v nacked=%v requeue=%v, want dead-lettered (nacked, requeue=false)", acked, nacked, requeue)
	}
}

// TestHandleRequeuesWhenRepublishInterruptedByShutdown proves that a re-enqueue
// cut short by a shutdown requeues the original (redelivered later) rather than
// dead-lettering work the shutdown, not the message, interrupted.
func TestHandleRequeuesWhenRepublishInterruptedByShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	d := &recDelivery{body: mustBody(t, validJob()), priority: 5}
	st := &fakeStore{err: errors.New("db down")} // transient, attempts remain
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, cancelingEnqueuer{cancel: cancel}, nil, Config{Concurrency: 1, MaxAttempts: 3})

	w.handle(ctx, d)

	acked, nacked, requeue := d.state()
	if acked || !nacked || !requeue {
		t.Fatalf("delivery acked=%v nacked=%v requeue=%v, want requeued (nacked, requeue=true)", acked, nacked, requeue)
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
		{"index too large", func(j *CrawlJob) { j.ChunkIndex = math.MaxInt32 + 1 }, false},
		{"empty content", func(j *CrawlJob) { j.Content = "" }, false},
		{"empty corpus", func(j *CrawlJob) { j.Corpus = "" }, false},
		{"bad kind", func(j *CrawlJob) { j.Kind = "sidebar" }, false},
		{"lead kind ok", func(j *CrawlJob) { j.Kind = "lead" }, true},
		{"negative revision", func(j *CrawlJob) { j.RevisionID = -1 }, false},
		{"negative attempt", func(j *CrawlJob) { j.Attempt = -1 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j := validJob()
			tc.mut(&j)
			if err := j.validate(); (err == nil) != tc.ok {
				t.Errorf("validate() err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}
