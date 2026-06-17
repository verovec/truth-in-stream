package factcheckarchive_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
)

// channelBroker is a tiny in-process stand-in for the RabbitMQ queue: the
// producer publishes job bodies onto a slice and the worker drains them through
// its real Process. It lets the e2e wire the real producer to the real worker
// without a live broker, exercising the whole publish -> consume -> embed ->
// upsert chain end to end.
type channelBroker struct {
	mu     sync.Mutex
	bodies [][]byte
	prio   []uint8
}

func (b *channelBroker) Publish(_ context.Context, body []byte, priority uint8) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bodies = append(b.bodies, body)
	b.prio = append(b.prio, priority)
	return nil
}

// upsertStore records every upserted claim keyed by ID, so the test can assert
// idempotency: a second run over the same archive rewrites the same keys rather
// than inserting duplicates.
type upsertStore struct {
	mu      sync.Mutex
	byID    map[string]domain.PoliticalClaim
	upserts int
}

func newUpsertStore() *upsertStore {
	return &upsertStore{byID: map[string]domain.PoliticalClaim{}}
}

func (s *upsertStore) UpsertPoliticalClaim(_ context.Context, claim domain.PoliticalClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[claim.ID] = claim
	s.upserts++
	return nil
}

// fixtureAPIServer serves the two-page claims:search archive fixtures.
func fixtureAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	page1, err := os.ReadFile("testdata/claims_page1.json")
	if err != nil {
		t.Fatalf("read page1: %v", err)
	}
	page2, err := os.ReadFile("testdata/claims_page2.json")
	if err != nil {
		t.Fatalf("read page2: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "CONTINUE_TOKEN_PAGE2" {
			_, _ = w.Write(page2)
			return
		}
		_, _ = w.Write(page1)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// drain runs the worker's real Process over every queued body into the store.
func drain(t *testing.T, broker *channelBroker, store *upsertStore) {
	t.Helper()
	// embed.NewDeterministic mirrors the real Voyage contract (EmbeddingDim
	// vectors, document/query parity) so the worker's dim check and the store's
	// halfvec width both see a real-shaped embedding.
	worker := factcheckjob.NewWorker(embed.NewDeterministic(domain.EmbeddingDim), store, nil, nil, nil,
		factcheckjob.Config{Concurrency: 1, MaxAttempts: 3})
	broker.mu.Lock()
	bodies := append([][]byte(nil), broker.bodies...)
	prio := append([]uint8(nil), broker.prio...)
	broker.mu.Unlock()
	for i, body := range bodies {
		if res := worker.Process(t.Context(), body, prio[i]); res.Action != factcheckjob.ActionAck {
			t.Fatalf("worker did not ack job %d, action=%v", i, res.Action)
		}
	}
}

// TestArchiveToClaimDBEndToEnd is the operator-level check the card requires: the
// real producer reads the fixture archive over HTTP, publishes self-contained
// jobs, the real worker embeds and upserts them, and the resulting curated claim
// records carry a verdict, a primary source, and an embedding. Re-running the
// whole pipeline is idempotent: the same claim IDs are rewritten, not duplicated.
func TestArchiveToClaimDBEndToEnd(t *testing.T) {
	t.Parallel()
	srv := fixtureAPIServer(t)
	producer, err := factcheckarchive.New(factcheckarchive.Config{
		BaseURL: srv.URL, APIKey: "test-key", LanguageCode: "fr", MaxPriority: 10,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store := newUpsertStore()

	// First run: produce -> drain.
	broker1 := &channelBroker{}
	if _, err := producer.Run(t.Context(), nil, broker1, factcheckarchive.RunConfig{Query: "élection"}); err != nil {
		t.Fatalf("first producer run: %v", err)
	}
	drain(t, broker1, store)

	if len(store.byID) != 4 {
		t.Fatalf("stored %d distinct claims, want 4", len(store.byID))
	}
	claim, ok := store.byID["https://factuel.afp.com/doc.afp.com.34PQ6WA"]
	if !ok {
		t.Fatal("AFP claim not stored")
	}
	if claim.LiteralVerdict != domain.LiteralInaccurate {
		t.Errorf("verdict = %q, want inaccurate", claim.LiteralVerdict)
	}
	if claim.SourceURL == "" || claim.SourceName == "" {
		t.Errorf("primary source missing: name=%q url=%q", claim.SourceName, claim.SourceURL)
	}
	if len(claim.Embedding) != domain.EmbeddingDim {
		t.Errorf("embedding dims = %d, want %d", len(claim.Embedding), domain.EmbeddingDim)
	}
	if claim.CheckedAt.IsZero() {
		t.Errorf("checked-at not populated")
	}

	// Second run over the same archive: idempotent. The distinct-ID count is
	// unchanged, even though every claim was upserted again.
	broker2 := &channelBroker{}
	if _, err := producer.Run(t.Context(), nil, broker2, factcheckarchive.RunConfig{Query: "élection"}); err != nil {
		t.Fatalf("second producer run: %v", err)
	}
	drain(t, broker2, store)

	if len(store.byID) != 4 {
		t.Fatalf("after re-run, %d distinct claims, want 4 (no duplicates)", len(store.byID))
	}
	if store.upserts != 8 {
		t.Fatalf("upserts = %d, want 8 (4 claims x 2 runs, all rewriting the same keys)", store.upserts)
	}
}
