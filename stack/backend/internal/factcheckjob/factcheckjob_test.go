package factcheckjob

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeEmbedder returns a fixed-dimension vector per text, or a configured error.
type fakeEmbedder struct {
	err  error
	dims int
}

func (f fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	dims := f.dims
	if dims == 0 {
		dims = domain.EmbeddingDim
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, dims)
	}
	return out, nil
}

// recordingStore captures every upserted claim and can be configured to fail, so
// a test can exercise the write-failure branch (a failed upsert must never ack).
type recordingStore struct {
	mu     sync.Mutex
	claims []domain.PoliticalClaim
	err    error
}

func (s *recordingStore) UpsertPoliticalClaim(_ context.Context, claim domain.PoliticalClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.claims = append(s.claims, claim)
	return nil
}

func (s *recordingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.claims)
}

func validJob() ClaimJob {
	return ClaimJob{
		ID:             "https://factuel.afp.com/doc.afp.com.34PQ6WA",
		Text:           "La criminalité a augmenté de 50% sous ce gouvernement.",
		LiteralVerdict: string(domain.LiteralInaccurate),
		Flags:          []string{string(domain.FlagCherryPicked)},
		SourceName:     "AFP Factuel",
		SourceURL:      "https://factuel.afp.com/doc.afp.com.34PQ6WA",
		QuotedSpan:     "La criminalité a augmenté de 50%",
		Outlet:         "factuel.afp.com",
		CheckedAt:      "2024-03-18T00:00:00Z",
	}
}

func mustEncode(t *testing.T, job ClaimJob) []byte {
	t.Helper()
	b, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	return b
}

func TestProcessUpsertsClaim(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	res := w.Process(t.Context(), mustEncode(t, validJob()), 5)
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck", res.Action)
	}
	if len(store.claims) != 1 {
		t.Fatalf("stored %d claims, want 1", len(store.claims))
	}
	got := store.claims[0]
	if got.ID != "https://factuel.afp.com/doc.afp.com.34PQ6WA" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.LiteralVerdict != domain.LiteralInaccurate {
		t.Errorf("verdict = %q, want inaccurate", got.LiteralVerdict)
	}
	if len(got.Flags) != 1 || got.Flags[0] != domain.FlagCherryPicked {
		t.Errorf("flags = %v", got.Flags)
	}
	if got.SourceName != "AFP Factuel" || got.Outlet != "factuel.afp.com" {
		t.Errorf("source/outlet = %q/%q", got.SourceName, got.Outlet)
	}
	if got.CheckedAt.IsZero() {
		t.Errorf("checked-at not parsed")
	}
	if len(got.Embedding) != domain.EmbeddingDim {
		t.Errorf("embedding dims = %d, want %d", len(got.Embedding), domain.EmbeddingDim)
	}
}

func TestProcessEmptyCheckedAtStoresZeroTime(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
	job := validJob()
	job.CheckedAt = ""

	res := w.Process(t.Context(), mustEncode(t, job), 5)
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck", res.Action)
	}
	if len(store.claims) != 1 || !store.claims[0].CheckedAt.IsZero() {
		t.Fatalf("empty checked-at should store the zero time")
	}
}

func TestProcessDropsMalformedJSON(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	res := w.Process(t.Context(), []byte("{not json"), 5)
	if res.Action != ActionReject {
		t.Fatalf("action = %v, want ActionReject (dead-letter poison)", res.Action)
	}
	if len(store.claims) != 0 {
		t.Fatalf("malformed job must not be stored")
	}
}

func TestProcessDropsInvalidJob(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ClaimJob){
		"empty id":         func(j *ClaimJob) { j.ID = "" },
		"empty text":       func(j *ClaimJob) { j.Text = "" },
		"bad verdict":      func(j *ClaimJob) { j.LiteralVerdict = "maybe" },
		"bad flag":         func(j *ClaimJob) { j.Flags = []string{"made-up"} },
		"bad checked-at":   func(j *ClaimJob) { j.CheckedAt = "not-a-date" },
		"negative attempt": func(j *ClaimJob) { j.Attempt = -1 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &recordingStore{}
			w := NewWorker(fakeEmbedder{}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
			job := validJob()
			mut(&job)
			res := w.Process(t.Context(), mustEncode(t, job), 5)
			if res.Action != ActionReject {
				t.Fatalf("action = %v, want ActionReject (dead-letter invalid)", res.Action)
			}
			if len(store.claims) != 0 {
				t.Fatalf("invalid job must not be stored")
			}
		})
	}
}

func TestProcessDropsOnUnexpectedEmbeddingShape(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{dims: 7}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	res := w.Process(t.Context(), mustEncode(t, validJob()), 5)
	if res.Action != ActionReject {
		t.Fatalf("action = %v, want ActionReject (dead-letter a provider-contract violation)", res.Action)
	}
	if len(store.claims) != 0 {
		t.Fatalf("wrong-dim embedding must not be stored")
	}
}

func TestProcessRepublishesOnTransientEmbedError(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{err: errors.New("429")}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	res := w.Process(t.Context(), mustEncode(t, validJob()), 5)
	if res.Action != ActionRepublish {
		t.Fatalf("action = %v, want ActionRepublish", res.Action)
	}
	var retry ClaimJob
	if err := json.Unmarshal(res.RepublishBody, &retry); err != nil {
		t.Fatalf("decode republished body: %v", err)
	}
	if retry.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", retry.Attempt)
	}
	if res.RepublishPriority != 5 {
		t.Fatalf("priority = %d, want 5", res.RepublishPriority)
	}
}

func TestProcessDeadLettersAfterExhaustingRetries(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{err: errors.New("boom")}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 2})
	job := validJob()
	job.Attempt = 1 // already on the last allowed attempt

	res := w.Process(t.Context(), mustEncode(t, job), 5)
	if res.Action != ActionReject {
		t.Fatalf("action = %v, want ActionReject (dead-letter after exhausting retries)", res.Action)
	}
}

func TestProcessRequeuesOnShutdown(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{err: errors.New("boom")}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res := w.Process(ctx, mustEncode(t, validJob()), 5)
	if res.Action != ActionRequeue {
		t.Fatalf("action = %v, want ActionRequeue", res.Action)
	}
}

// TestProcessRepublishesOnUpsertFailure pins the ack-after-write property on the
// write side: when the upsert itself fails transiently, the delivery is NOT acked
// but re-enqueued for a bounded retry, so a failed DB write can never drop a claim.
func TestProcessRepublishesOnUpsertFailure(t *testing.T) {
	t.Parallel()
	store := &recordingStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	res := w.Process(t.Context(), mustEncode(t, validJob()), 5)
	if res.Action != ActionRepublish {
		t.Fatalf("action = %v, want ActionRepublish on a failed upsert", res.Action)
	}
	var retry ClaimJob
	if err := json.Unmarshal(res.RepublishBody, &retry); err != nil {
		t.Fatalf("decode republished body: %v", err)
	}
	if retry.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", retry.Attempt)
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
	d := &recDelivery{body: mustEncode(t, validJob()), priority: 5}
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{}, store, &sliceStream{[]Delivery{d}}, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, nacked, _ := d.state()
	if !acked || nacked {
		t.Fatalf("delivery acked=%v nacked=%v, want acked after a committed upsert", acked, nacked)
	}
	if store.count() != 1 {
		t.Fatalf("upsert did not run before ack: stored %d claims, want 1", store.count())
	}
}

// TestHandleNacksRequeueOnShutdown proves an in-flight delivery interrupted by a
// shutdown (canceled context, so embed fails) is nacked WITH requeue and never
// acked, so the broker redelivers it without the worker burning an attempt.
func TestHandleNacksRequeueOnShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d := &recDelivery{body: mustEncode(t, validJob()), priority: 5}
	w := NewWorker(fakeEmbedder{err: context.Canceled}, &recordingStore{}, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	w.handle(ctx, d)

	acked, nacked, requeue := d.state()
	if acked {
		t.Fatal("delivery acked during shutdown; interrupted work would be lost")
	}
	if !nacked || !requeue {
		t.Fatalf("delivery nacked=%v requeue=%v, want nacked with requeue=true", nacked, requeue)
	}
}

func TestProcessIsIdempotentAcrossRedelivery(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
	body := mustEncode(t, validJob())

	w.Process(t.Context(), body, 5)
	w.Process(t.Context(), body, 5)
	// Same ID twice: the upsert is idempotent at the store layer, so the worker
	// emits the same claim ID both times (no duplicate-suppression in the worker).
	if len(store.claims) != 2 {
		t.Fatalf("processed twice, store recorded %d (store dedups by ID upstream)", len(store.claims))
	}
	if store.claims[0].ID != store.claims[1].ID {
		t.Fatalf("redelivery must carry the same claim ID")
	}
}
