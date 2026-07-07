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
	got domain.WikiChunk
	err error
}

func (f *fakeStore) UpsertEmbeddedChunk(_ context.Context, c domain.WikiChunk) error {
	f.got = c
	return f.err
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
	if st.got.PageID != 5 || st.got.Kind != domain.WikiChunkKindBody || len(st.got.Embedding) != domain.EmbeddingDim {
		t.Errorf("upserted chunk wrong: %+v", st.got)
	}
	if st.got.Title != "Atom" || st.got.URL != "u" || st.got.RevisionID != 9 || st.got.Corpus != "simplewiki-crawl" {
		t.Errorf("upserted chunk metadata wrong: %+v", st.got)
	}
}

func TestProcessMalformedIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	if res := w.Process(t.Context(), []byte("{not json"), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessInvalidJobIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	bad := validJob()
	bad.Content = ""
	if res := w.Process(t.Context(), mustBody(t, bad), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessWrongDimIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{vec: [][]float32{{0.1, 0.2}}}, &fakeStore{}, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(t.Context(), mustBody(t, validJob()), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
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

func TestProcessExhaustedAttemptsDropped(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 2})
	j := validJob()
	j.Attempt = 1 // already at budget-1
	if res := w.Process(t.Context(), mustBody(t, j), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop after retries)", res.Action)
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
// (page_id, chunk_index) (proven by store.TestUpsertEmbeddedChunkIsIdempotent).
func TestProcessRedeliveryIsIdempotent(t *testing.T) {
	st := &fakeStore{}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
	body := mustBody(t, validJob())

	first := w.Process(t.Context(), body, 5)
	firstKey := [2]int64{st.got.PageID, int64(st.got.ChunkIndex)}
	second := w.Process(t.Context(), body, 5)
	secondKey := [2]int64{st.got.PageID, int64(st.got.ChunkIndex)}

	if first.Action != ActionAck || second.Action != ActionAck {
		t.Fatalf("actions = %v, %v; want both ActionAck", first.Action, second.Action)
	}
	if firstKey != secondKey || firstKey != [2]int64{5, 1} {
		t.Fatalf("redelivery upserted keys %v then %v, want both (page 5, chunk 1)", firstKey, secondKey)
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
	if st.got.PageID != 5 {
		t.Fatalf("upsert did not run before ack: stored page id = %d", st.got.PageID)
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
