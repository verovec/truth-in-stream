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
	mu        sync.Mutex
	records   []domain.VotingRecord
	calls     int
	lastBatch int
	failN     int
	failWith  error
}

// UpsertVotingRecords models the real store's atomic apply: on failure it records
// nothing (a rolled-back transaction), and on success it appends the whole batch.
// It counts calls and the last batch size so a test can prove the worker applies
// a scrutin's records in one call, not one per record.
func (s *recordingStore) UpsertVotingRecords(_ context.Context, records []domain.VotingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastBatch = len(records)
	if s.failN > 0 {
		s.failN--
		return s.failWith
	}
	s.records = append(s.records, records...)
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

// senatPayload is a self-contained Senat scrutin the parliament producer publishes
// (the chamber-aware branch), naming two senators.
const senatPayload = `{
  "scrutin_id": "senat-2026-15",
  "objet": "sur l'ensemble du projet de loi X",
  "date": "2026-02-10",
  "source_url": "https://www.senat.fr/scrutin-public/2026/scr2026-15.html",
  "votes": [
    {"person_id": "98046X", "person_name": "François MARC", "position": "contre"},
    {"person_id": "98047Y", "person_name": "Yves FRÉVILLE", "position": "pour"}
  ]
}`

// TestProcessSenatChamberWritesSenatRecords proves the chamber-aware dispatch: a job
// stamped chamber=senat is parsed by the Senat parser and written with
// domain.ChamberSenat, while the existing Assemblee path (empty chamber) is
// unchanged.
func TestProcessSenatChamberWritesSenatRecords(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := newTestWorker(store)

	body, err := json.Marshal(ScrutinJob{ID: "senat-2026-15", Chamber: "senat", Scrutin: json.RawMessage(senatPayload)})
	if err != nil {
		t.Fatalf("marshal senat job: %v", err)
	}
	if res := w.Process(t.Context(), body, 5); res.Action != ActionAck {
		t.Fatalf("Action = %v, want ActionAck", res.Action)
	}
	if store.count() != 2 {
		t.Fatalf("upserted %d records, want 2", store.count())
	}
	for _, r := range store.records {
		if r.Chamber != domain.ChamberSenat {
			t.Errorf("record %q chamber = %q, want senat", r.PersonID, r.Chamber)
		}
		if r.ScrutinID != "senat-2026-15" {
			t.Errorf("record scrutin id = %q", r.ScrutinID)
		}
	}
}

// TestProcessUnknownChamberIsDeadLettered proves a job stamped with a chamber the
// worker does not know is dead-lettered, never written under the wrong parser.
func TestProcessUnknownChamberIsDeadLettered(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := newTestWorker(store)
	body, err := json.Marshal(ScrutinJob{ID: "x", Chamber: "bundestag", Scrutin: json.RawMessage(`{"scrutin_id":"x"}`)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if res := w.Process(t.Context(), body, 5); res.Action != ActionReject {
		t.Fatalf("Action = %v, want ActionReject for an unknown chamber", res.Action)
	}
	if store.count() != 0 {
		t.Fatalf("wrote %d records for an unknown chamber, want 0", store.count())
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

func TestProcessDeadLettersBadJobs(t *testing.T) {
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
			if res := w.Process(t.Context(), tc.body, 5); res.Action != ActionReject {
				t.Fatalf("Action = %v, want ActionReject (dead-letter a bad job)", res.Action)
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

func TestProcessDeadLettersAfterExhaustingRetries(t *testing.T) {
	t.Parallel()
	store := &recordingStore{failN: 1, failWith: errors.New("db down")}
	w := NewWorker(store, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	// Attempt 2 (zero-indexed) is the last allowed attempt for MaxAttempts 3.
	res := w.Process(t.Context(), jobBody(t, scrutinPayload, 2), 7)

	if res.Action != ActionReject {
		t.Fatalf("Action = %v, want ActionReject (dead-letter after retries)", res.Action)
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
// Run drains a delivery whose voting records all upsert, acks it (never nacks),
// and returns once the stream closes - a bounded exit with no leaked delivery.
func TestRunAcksAfterUpsertAndReturns(t *testing.T) {
	t.Parallel()
	d := &recDelivery{body: jobBody(t, scrutinPayload, 0), priority: 5}
	store := &recordingStore{}
	w := NewWorker(store, &sliceStream{[]Delivery{d}}, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, nacked, _ := d.state()
	if !acked || nacked {
		t.Fatalf("delivery acked=%v nacked=%v, want acked after committed upserts", acked, nacked)
	}
	if store.count() != 2 {
		t.Fatalf("upserts did not run before ack: stored %d records, want 2", store.count())
	}
}

// TestProcessAppliesRecordsInOneAtomicCall proves the worker hands a scrutin's
// whole record set to the store in a single call rather than one call per record,
// so the store can apply them atomically and a reader never sees a partial vote
// set. The per-row-in-a-transaction visibility itself is covered by the postgres
// store's concurrent-read integration test.
func TestProcessAppliesRecordsInOneAtomicCall(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	w := newTestWorker(store)

	if res := w.Process(t.Context(), jobBody(t, scrutinPayload, 0), 5); res.Action != ActionAck {
		t.Fatalf("Action = %v, want ActionAck", res.Action)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1 (one atomic apply, not one per record)", store.calls)
	}
	if store.lastBatch != 2 {
		t.Fatalf("apply batch = %d records, want 2 (the whole scrutin in one call)", store.lastBatch)
	}
}

// TestFailedApplyRecordsNothing proves the modeled atomic apply leaves no partial
// state when it fails, and the worker re-enqueues the job for a bounded retry.
func TestFailedApplyRecordsNothing(t *testing.T) {
	t.Parallel()
	store := &recordingStore{failN: 1, failWith: errors.New("db down")}
	w := newTestWorker(store)

	if res := w.Process(t.Context(), jobBody(t, scrutinPayload, 0), 5); res.Action != ActionRepublish {
		t.Fatalf("Action = %v, want ActionRepublish (transient failure retries)", res.Action)
	}
	if store.count() != 0 {
		t.Fatalf("stored %d records after a failed apply, want 0 (no partial vote set)", store.count())
	}
}

// TestStatsCountsProcessedAndParked proves the drain counters separate acked
// (processed) deliveries from dead-lettered (parked) ones - the counts the
// consumer run alert reports - across a good delivery and a poison one.
func TestStatsCountsProcessedAndParked(t *testing.T) {
	t.Parallel()
	good := &recDelivery{body: jobBody(t, scrutinPayload, 0), priority: 5, version: "1"}
	poison := &recDelivery{body: []byte("{not json"), priority: 5, version: "1"}
	store := &recordingStore{}
	w := NewWorker(store, &sliceStream{[]Delivery{good, poison}}, nil, nil,
		Config{Concurrency: 1, MaxAttempts: 3, KnownVersions: []string{"1"}})

	if err := w.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	stats := w.Stats()
	if stats.Processed != 1 {
		t.Errorf("Stats.Processed = %d, want 1 (the good delivery acked)", stats.Processed)
	}
	if stats.ParkedToDLQ != 1 {
		t.Errorf("Stats.ParkedToDLQ = %d, want 1 (the malformed delivery dead-lettered)", stats.ParkedToDLQ)
	}
}

// TestHandleNacksRequeueOnShutdown proves an in-flight delivery interrupted by a
// shutdown (canceled context) is nacked WITH requeue and never acked, so the
// broker redelivers it without the worker burning an attempt.
func TestHandleNacksRequeueOnShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d := &recDelivery{body: jobBody(t, scrutinPayload, 0), priority: 5}
	w := newTestWorker(&recordingStore{})

	w.handle(ctx, d)

	acked, nacked, requeue := d.state()
	if acked {
		t.Fatal("delivery acked during shutdown; interrupted work would be lost")
	}
	if !nacked || !requeue {
		t.Fatalf("delivery nacked=%v requeue=%v, want nacked with requeue=true", nacked, requeue)
	}
}
