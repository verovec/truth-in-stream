package scrutinsjob

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// scrutinPayload is the bare inner scrutin object (without the {"scrutin": ...}
// envelope) the producer puts in a job. It names two voters so the worker writes
// two records and idempotency over (person, scrutin) is observable.
const scrutinPayload = `{
  "uid": "VTANR5L17V42",
  "numero": "42",
  "legislature": "17",
  "dateScrutin": "2024-10-15",
  "objet": {"libelle": "sur l'ensemble du projet de loi de finances pour 2025"},
  "ventilationVotes": {"organe": {"groupes": {"groupe": [
    {"vote": {"decompteNominatif": {
      "pours": {"votant": [{"acteurRef": "PA1592"}]},
      "contres": {"votant": {"acteurRef": "PA721002"}}
    }}}
  ]}}}
}`

func jobBody(t *testing.T, payload string, attempt int) []byte {
	t.Helper()
	body, err := json.Marshal(ScrutinJob{ID: "VTANR5L17V42", Scrutin: json.RawMessage(payload), Attempt: attempt})
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	return body
}

// recordingStore captures every upsert and can fail a configured number of times
// to exercise the retry path.
type recordingStore struct {
	mu       sync.Mutex
	records  []domain.VotingRecord
	failN    int
	failWith error
}

func (s *recordingStore) UpsertVotingRecord(_ context.Context, r domain.VotingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failN > 0 {
		s.failN--
		return s.failWith
	}
	s.records = append(s.records, r)
	return nil
}

func (s *recordingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func newTestWorker(store Store) *Worker {
	return NewWorker(store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
}

func TestProcessParsesAndUpserts(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := newTestWorker(store)

	res := w.Process(t.Context(), jobBody(t, scrutinPayload, 0), 5)

	if res.Action != ActionAck {
		t.Fatalf("Action = %v, want ActionAck", res.Action)
	}
	if store.count() != 2 {
		t.Fatalf("upserted %d records, want 2", store.count())
	}
	got := store.records[0]
	if got.PersonID != "PA1592" || got.Position != domain.VoteFor || got.ScrutinID != "VTANR5L17V42" {
		t.Fatalf("first record = %+v, want PA1592/for/VTANR5L17V42", got)
	}
	if got.Chamber != domain.ChamberAssemblee {
		t.Fatalf("chamber = %q, want %q", got.Chamber, domain.ChamberAssemblee)
	}
}

func TestProcessIsIdempotent(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := newTestWorker(store)
	body := jobBody(t, scrutinPayload, 0)

	for i := range 3 {
		if res := w.Process(t.Context(), body, 5); res.Action != ActionAck {
			t.Fatalf("attempt %d Action = %v, want ActionAck", i, res.Action)
		}
	}
	// The store is a recorder, not a real upsert, so each redelivery re-emits the
	// same rows; what matters is the worker always Acks and writes the same keys -
	// the (person, scrutin) idempotency lives in the real store's upsert.
	if store.count() != 6 {
		t.Fatalf("recorded %d writes over 3 deliveries, want 6", store.count())
	}
	for _, r := range store.records {
		if r.ScrutinID != "VTANR5L17V42" {
			t.Fatalf("record for unexpected scrutin %q", r.ScrutinID)
		}
	}
}

func TestProcessDropsBadJobs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body []byte
	}{
		{"malformed json", []byte("{not json")},
		{"empty id", func() []byte {
			b, _ := json.Marshal(ScrutinJob{Scrutin: json.RawMessage(scrutinPayload)})
			return b
		}()},
		{"empty payload", func() []byte {
			b, _ := json.Marshal(ScrutinJob{ID: "x"})
			return b
		}()},
		{"unparseable scrutin", jobBody(t, `{"uid":"x"}`, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &recordingStore{}
			w := newTestWorker(store)
			if res := w.Process(t.Context(), tc.body, 5); res.Action != ActionAck {
				t.Fatalf("Action = %v, want ActionAck (drop)", res.Action)
			}
			if store.count() != 0 {
				t.Fatalf("wrote %d records for a bad job, want 0", store.count())
			}
		})
	}
}

func TestProcessRepublishesOnTransientFailure(t *testing.T) {
	t.Parallel()
	store := &recordingStore{failN: 1, failWith: errors.New("db down")}
	w := NewWorker(store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	res := w.Process(t.Context(), jobBody(t, scrutinPayload, 0), 7)

	if res.Action != ActionRepublish {
		t.Fatalf("Action = %v, want ActionRepublish", res.Action)
	}
	if res.RepublishPriority != 7 {
		t.Fatalf("republish priority = %d, want 7", res.RepublishPriority)
	}
	var retry ScrutinJob
	if err := json.Unmarshal(res.RepublishBody, &retry); err != nil {
		t.Fatalf("decode republish body: %v", err)
	}
	if retry.Attempt != 1 {
		t.Fatalf("retry attempt = %d, want 1", retry.Attempt)
	}
}

func TestProcessDropsAfterExhaustingRetries(t *testing.T) {
	t.Parallel()
	store := &recordingStore{failN: 1, failWith: errors.New("db down")}
	w := NewWorker(store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	// Attempt 2 (zero-indexed) is the last allowed attempt for MaxAttempts 3.
	res := w.Process(t.Context(), jobBody(t, scrutinPayload, 2), 7)

	if res.Action != ActionAck {
		t.Fatalf("Action = %v, want ActionAck (drop after retries)", res.Action)
	}
}

func TestProcessRequeuesOnShutdown(t *testing.T) {
	t.Parallel()
	store := &recordingStore{failN: 1, failWith: errors.New("db down")}
	w := newTestWorker(store)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res := w.Process(ctx, jobBody(t, scrutinPayload, 0), 7)

	if res.Action != ActionRequeue {
		t.Fatalf("Action = %v, want ActionRequeue", res.Action)
	}
}
