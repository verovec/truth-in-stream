package postgres

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func politicalClaim(id, text string, emb []float32) domain.PoliticalClaim {
	return domain.PoliticalClaim{
		ID:             id,
		Text:           text,
		LiteralVerdict: domain.LiteralAccurate,
		Flags:          []domain.ManipulationFlag{domain.FlagCherryPicked, domain.FlagMissingContext},
		SourceName:     "INSEE",
		SourceURL:      "https://insee.fr/serie",
		QuotedSpan:     "le chomage a baisse",
		Outlet:         "Les Decodeurs",
		CheckedAt:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Embedding:      emb,
	}
}

func TestSearchPoliticalClaimsOrdersByCosineAndReturnsVerdictAndSource(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.UpsertPoliticalClaim(ctx, politicalClaim("alpha", "alpha claim", unitVec(0))); err != nil {
		t.Fatalf("UpsertPoliticalClaim alpha: %v", err)
	}
	if err := store.UpsertPoliticalClaim(ctx, politicalClaim("bravo", "bravo claim", unitVec(1))); err != nil {
		t.Fatalf("UpsertPoliticalClaim bravo: %v", err)
	}

	tests := []struct {
		name      string
		query     []float32
		topK      int
		wantFirst string
		wantLen   int
	}{
		{name: "nearest is alpha", query: unitVec(0), topK: 5, wantFirst: "alpha", wantLen: 2},
		{name: "nearest is bravo", query: unitVec(1), topK: 5, wantFirst: "bravo", wantLen: 2},
		{name: "topK truncates", query: unitVec(0), topK: 1, wantFirst: "alpha", wantLen: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.SearchPoliticalClaims(ctx, tc.query, tc.topK)
			if err != nil {
				t.Fatalf("SearchPoliticalClaims: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("got %d matches, want %d", len(got), tc.wantLen)
			}
			if got[0].ID != tc.wantFirst {
				t.Fatalf("nearest = %q, want %q", got[0].ID, tc.wantFirst)
			}
			if got[0].Distance > 1e-4 {
				t.Errorf("nearest distance = %v, want ~0", got[0].Distance)
			}
			m := got[0]
			if m.LiteralVerdict != domain.LiteralAccurate {
				t.Errorf("verdict = %q, want accurate", m.LiteralVerdict)
			}
			if diff := cmp.Diff([]domain.ManipulationFlag{domain.FlagCherryPicked, domain.FlagMissingContext}, m.Flags); diff != "" {
				t.Errorf("flags mismatch (-want +got):\n%s", diff)
			}
			if m.SourceName != "INSEE" || m.SourceURL != "https://insee.fr/serie" {
				t.Errorf("source = %q/%q, want INSEE/https://insee.fr/serie", m.SourceName, m.SourceURL)
			}
			if m.QuotedSpan != "le chomage a baisse" || m.Outlet != "Les Decodeurs" {
				t.Errorf("span/outlet = %q/%q", m.QuotedSpan, m.Outlet)
			}
			if !m.CheckedAt.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
				t.Errorf("checked_at = %v, want 2026-01-02", m.CheckedAt)
			}
		})
	}
}

func TestUpsertPoliticalClaimIsIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	first := politicalClaim("dup", "first text", unitVec(0))
	if err := store.UpsertPoliticalClaim(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	updated := first
	updated.Text = "second text"
	updated.LiteralVerdict = domain.LiteralInaccurate
	updated.Flags = nil
	if err := store.UpsertPoliticalClaim(ctx, updated); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := store.SearchPoliticalClaims(ctx, unitVec(0), 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 (upsert must replace, not insert)", len(got))
	}
	if got[0].Text != "second text" || got[0].LiteralVerdict != domain.LiteralInaccurate {
		t.Errorf("row not updated: %+v", got[0])
	}
	if len(got[0].Flags) != 0 {
		t.Errorf("flags = %v, want empty after update", got[0].Flags)
	}
}

func TestUpsertPoliticalClaimRejectsBadInput(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	tests := []struct {
		name  string
		claim domain.PoliticalClaim
	}{
		{
			name: "invalid verdict",
			claim: func() domain.PoliticalClaim {
				c := politicalClaim("x", "t", unitVec(0))
				c.LiteralVerdict = "true"
				return c
			}(),
		},
		{
			name: "invalid flag",
			claim: func() domain.PoliticalClaim {
				c := politicalClaim("x", "t", unitVec(0))
				c.Flags = []domain.ManipulationFlag{"biased"}
				return c
			}(),
		},
		{
			name: "wrong embedding dim",
			claim: func() domain.PoliticalClaim {
				c := politicalClaim("x", "t", []float32{1, 2, 3})
				return c
			}(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.UpsertPoliticalClaim(ctx, tc.claim); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestPoliticalClaimEmbeddingRoundTripsTextForm asserts the halfvec is written
// and read back through the text-form ::halfvec path (the pgvector Valuer), never
// binary COPY, by confirming the stored vector matches the input exactly.
func TestPoliticalClaimEmbeddingRoundTripsTextForm(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	emb := unitVec(7)
	if err := store.UpsertPoliticalClaim(ctx, politicalClaim("vec", "vector claim", emb)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// The text representation of the stored halfvec must match the input exactly:
	// a binary-COPY corruption would surface as a different (or NULL/phantom) row.
	var text string
	if err := store.pool.QueryRow(ctx, "SELECT embedding::text FROM political_claims WHERE id = $1", "vec").Scan(&text); err != nil {
		t.Fatalf("read embedding text: %v", err)
	}
	if text == "" {
		t.Fatal("stored embedding is empty")
	}
	// Nearest-neighbor to itself must be distance ~0.
	got, err := store.SearchPoliticalClaims(ctx, emb, 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].Distance > 1e-4 {
		t.Fatalf("self-match distance not ~0: %+v", got)
	}
}

// TestPoliticalClaimFlagsCheckConstraint proves the DB rejects an unknown flag
// even when a writer bypasses the Go-side marshalFlags validation, so the column
// has the same second line of defense as literal_verdict.
func TestPoliticalClaimFlagsCheckConstraint(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	emb := pgvector.NewHalfVector(unitVec(0))
	_, err := store.pool.Exec(
		ctx,
		`INSERT INTO political_claims (id, content, literal_verdict, flags, source_name, source_url, outlet, embedding)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		"bad", "t", "accurate", []string{"biased"}, "INSEE", "https://insee.fr", "Les Decodeurs", emb,
	)
	if err == nil {
		t.Fatal("expected CHECK violation for unknown flag written directly via SQL")
	}
}

func votingRecord(personID, scrutinID, bill string, votedOn time.Time, pos domain.VotePosition) domain.VotingRecord {
	return domain.VotingRecord{
		PersonID:   personID,
		PersonName: "Jean Depute",
		Chamber:    domain.ChamberAssemblee,
		ScrutinID:  scrutinID,
		BillTitle:  bill,
		VotedOn:    votedOn,
		Position:   pos,
		SourceURL:  "https://assemblee-nationale.fr/scrutin/" + scrutinID,
	}
}

func TestLookupVotingRecordsByPersonBillDate(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	day := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	other := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	records := []domain.VotingRecord{
		votingRecord("p1", "s1", "Loi climat", day, domain.VoteFor),
		votingRecord("p1", "s2", "Loi budget", day, domain.VoteAgainst),   // same person+date, different bill
		votingRecord("p1", "s3", "Loi climat", other, domain.VoteAbstain), // same person+bill, different date
		votingRecord("p2", "s4", "Loi climat", day, domain.VoteAbsent),    // different person
	}
	for _, r := range records {
		if err := store.UpsertVotingRecord(ctx, r); err != nil {
			t.Fatalf("UpsertVotingRecord %s: %v", r.ScrutinID, err)
		}
	}

	got, err := store.LookupVotingRecords(ctx, "p1", "Loi climat", day)
	if err != nil {
		t.Fatalf("LookupVotingRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1 (predicate must filter on all three)", len(got))
	}
	want := domain.VotingRecord{
		PersonID:   "p1",
		PersonName: "Jean Depute",
		Chamber:    domain.ChamberAssemblee,
		ScrutinID:  "s1",
		BillTitle:  "Loi climat",
		VotedOn:    day,
		Position:   domain.VoteFor,
		SourceURL:  "https://assemblee-nationale.fr/scrutin/s1",
	}
	if diff := cmp.Diff(want, got[0]); diff != "" {
		t.Errorf("record mismatch (-want +got):\n%s", diff)
	}
}

func TestUpsertVotingRecordIsIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	day := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	rec := votingRecord("p1", "s1", "Loi climat", day, domain.VoteFor)
	if err := store.UpsertVotingRecord(ctx, rec); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	rec.Position = domain.VoteAgainst
	if err := store.UpsertVotingRecord(ctx, rec); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := store.LookupVotingRecords(ctx, "p1", "Loi climat", day)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1 (upsert must replace by person+scrutin)", len(got))
	}
	if got[0].Position != domain.VoteAgainst {
		t.Errorf("position = %q, want against after re-upsert", got[0].Position)
	}
}

func TestUpsertVotingRecordRejectsBadInput(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	day := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		record domain.VotingRecord
	}{
		{
			name: "invalid chamber",
			record: func() domain.VotingRecord {
				r := votingRecord("p1", "s1", "Loi", day, domain.VoteFor)
				r.Chamber = "congress"
				return r
			}(),
		},
		{
			name: "invalid position",
			record: func() domain.VotingRecord {
				r := votingRecord("p1", "s1", "Loi", day, "yes")
				return r
			}(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.UpsertVotingRecord(ctx, tc.record); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}
