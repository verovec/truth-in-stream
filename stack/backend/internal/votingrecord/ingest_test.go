package votingrecord_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/votingrecord"
)

// memStore is an in-memory domain.VotingStore keyed exactly as the table is
// (person_id, scrutin_id), so upserting the same scrutin twice overwrites rather
// than duplicates - the property the ingest's idempotency rests on.
type memStore struct {
	rows  map[[2]string]domain.VotingRecord
	calls int
}

func newMemStore() *memStore {
	return &memStore{rows: make(map[[2]string]domain.VotingRecord)}
}

func (m *memStore) UpsertVotingRecord(_ context.Context, r domain.VotingRecord) error {
	m.calls++
	m.rows[[2]string{r.PersonID, r.ScrutinID}] = r
	return nil
}

func (m *memStore) LookupVotingRecords(_ context.Context, personID, billTitle string, votedOn time.Time) ([]domain.VotingRecord, error) {
	var out []domain.VotingRecord
	for _, r := range m.rows {
		if r.PersonID == personID && r.BillTitle == billTitle && r.VotedOn.Equal(votedOn) {
			out = append(out, r)
		}
	}
	return out, nil
}

func writeFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile("testdata/VTANR5L17V42.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VTANR5L17V42.json"), raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// A non-JSON sibling must be ignored, not parsed.
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("ignore me"), 0o600); err != nil {
		t.Fatalf("write sibling: %v", err)
	}
	return dir
}

func TestIngestDirPopulatesStoreAndLookup(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	dir := writeFixtureDir(t)

	summary, err := votingrecord.IngestDir(t.Context(), store, dir)
	if err != nil {
		t.Fatalf("IngestDir: %v", err)
	}
	if summary.Files != 1 {
		t.Errorf("Files = %d, want 1", summary.Files)
	}
	if summary.Records != 5 {
		t.Errorf("Records = %d, want 5", summary.Records)
	}

	votedOn := time.Date(2024, 10, 15, 0, 0, 0, 0, time.UTC)
	bill := "sur l'ensemble du projet de loi de finances pour 2025 (première lecture)."

	got, err := store.LookupVotingRecords(t.Context(), "PA721002", bill, votedOn)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("lookup returned %d records, want 1", len(got))
	}
	if got[0].Position != domain.VoteAgainst {
		t.Errorf("Position = %q, want against", got[0].Position)
	}
	if got[0].SourceURL != "https://www.assemblee-nationale.fr/dyn/17/scrutins/42" {
		t.Errorf("SourceURL = %q", got[0].SourceURL)
	}
}

func TestIngestDirIdempotent(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	dir := writeFixtureDir(t)

	if _, err := votingrecord.IngestDir(t.Context(), store, dir); err != nil {
		t.Fatalf("first IngestDir: %v", err)
	}
	if _, err := votingrecord.IngestDir(t.Context(), store, dir); err != nil {
		t.Fatalf("second IngestDir: %v", err)
	}

	if len(store.rows) != 5 {
		t.Errorf("after re-ingest store holds %d rows, want 5 (re-run must not duplicate)", len(store.rows))
	}
}

func TestIngestDirEmptyDir(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	summary, err := votingrecord.IngestDir(t.Context(), store, t.TempDir())
	if err != nil {
		t.Fatalf("IngestDir over empty dir: %v", err)
	}
	if summary.Files != 0 || summary.Records != 0 {
		t.Errorf("empty dir summary = %+v, want zero", summary)
	}
}

func TestIngestDirMissingDir(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	if _, err := votingrecord.IngestDir(t.Context(), store, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for a missing directory")
	}
}
