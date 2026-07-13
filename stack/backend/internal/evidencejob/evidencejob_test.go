package evidencejob

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
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

// chunkKey is the generic evidence natural key the worker upserts under.
type chunkKey struct {
	source     string
	externalID string
	chunkIndex int
}

func fullVec() []float32 { return make([]float32, domain.EmbeddingDim) }

func validJob() connector.EvidenceJob {
	return connector.EvidenceJob{
		Source:     "an-amendements",
		ExternalID: "AMANR5L17PO838901B0324P1D1N002215",
		ChunkIndex: 0,
		Title:      "Amendement CL2215",
		URL:        "https://www.assemblee-nationale.fr/dyn/17/amendements/0324/CION_LOIS/CL2215",
		Content:    "M. Dupont a depose l'amendement CL2215 (Adopte).",
		Kind:       string(domain.EvidenceKindLead),
		Metadata:   map[string]any{"sort": "Adopte"},
	}
}

func mustBody(t *testing.T, j connector.EvidenceJob) []byte {
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
	if st.got.Source != "an-amendements" || st.got.ExternalID != "AMANR5L17PO838901B0324P1D1N002215" ||
		st.got.Kind != domain.EvidenceKindLead || len(st.got.Embedding) != domain.EmbeddingDim {
		t.Errorf("upserted chunk wrong: %+v", st.got)
	}
	if st.got.Metadata["sort"] != "Adopte" {
		t.Errorf("metadata not carried verbatim: %+v", st.got.Metadata)
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
	var retried connector.EvidenceJob
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
// (source, external_id, chunk_index).
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
	want := chunkKey{"an-amendements", "AMANR5L17PO838901B0324P1D1N002215", 0}
	if firstKey != secondKey || firstKey != want {
		t.Fatalf("redelivery upserted keys %v then %v, want both %v", firstKey, secondKey, want)
	}
}

// --- Run/handle loop: ack-after-write and shutdown requeue ---

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
	if st.got.ExternalID != "AMANR5L17PO838901B0324P1D1N002215" {
		t.Fatalf("upsert did not run before ack: stored external id = %q", st.got.ExternalID)
	}
}

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

type fakeEnqueuer struct{ err error }

func (f fakeEnqueuer) Enqueue(_ context.Context, _ []byte, _ uint8) error { return f.err }

type cancelingEnqueuer struct{ cancel context.CancelFunc }

func (e cancelingEnqueuer) Enqueue(_ context.Context, _ []byte, _ uint8) error {
	e.cancel()
	return context.Canceled
}

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

func TestHandleDeadLettersWhenRepublishFails(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: mustBody(t, validJob()), priority: 5}
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, fakeEnqueuer{err: errors.New("broker down")}, nil, Config{Concurrency: 1, MaxAttempts: 3})

	w.handle(t.Context(), d)

	acked, nacked, requeue := d.state()
	if acked || !nacked || requeue {
		t.Fatalf("delivery acked=%v nacked=%v requeue=%v, want dead-lettered (nacked, requeue=false)", acked, nacked, requeue)
	}
}

func TestHandleRequeuesWhenRepublishInterruptedByShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	d := &recDelivery{body: mustBody(t, validJob()), priority: 5}
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, cancelingEnqueuer{cancel: cancel}, nil, Config{Concurrency: 1, MaxAttempts: 3})

	w.handle(ctx, d)

	acked, nacked, requeue := d.state()
	if acked || !nacked || !requeue {
		t.Fatalf("delivery acked=%v nacked=%v requeue=%v, want requeued (nacked, requeue=true)", acked, nacked, requeue)
	}
}
