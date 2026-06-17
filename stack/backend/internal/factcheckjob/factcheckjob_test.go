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

// recordingStore captures every upserted claim.
type recordingStore struct {
	mu     sync.Mutex
	claims []domain.PoliticalClaim
}

func (s *recordingStore) UpsertPoliticalClaim(_ context.Context, claim domain.PoliticalClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, claim)
	return nil
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
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck (drop poison)", res.Action)
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
			if res.Action != ActionAck {
				t.Fatalf("action = %v, want ActionAck", res.Action)
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
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck", res.Action)
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

func TestProcessDropsAfterExhaustingRetries(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := NewWorker(fakeEmbedder{err: errors.New("boom")}, store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 2})
	job := validJob()
	job.Attempt = 1 // already on the last allowed attempt

	res := w.Process(t.Context(), mustEncode(t, job), 5)
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want ActionAck (exhausted)", res.Action)
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
