package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// Store satisfies the political evidence ports.
var (
	_ domain.PoliticalClaimStore = (*Store)(nil)
	_ domain.VotingStore         = (*Store)(nil)
)

// UpsertPoliticalClaim inserts or replaces one curated political claim together
// with its embedding in a single statement, so the row is never visible to ANN
// search without a matching vector. The embedding is written text-form
// (::halfvec via the pgvector Valuer), never binary COPY. The verdict and flags
// are validated against the domain enums before insert so they cannot violate
// the column CHECK constraints.
func (s *Store) UpsertPoliticalClaim(ctx context.Context, claim domain.PoliticalClaim) error {
	if !claim.LiteralVerdict.Valid() {
		return fmt.Errorf("postgres: upsert political claim %q: invalid literal verdict %q", claim.ID, claim.LiteralVerdict)
	}
	if len(claim.Embedding) != domain.EmbeddingDim {
		return fmt.Errorf("postgres: upsert political claim %q: embedding has %d dims, want %d", claim.ID, len(claim.Embedding), domain.EmbeddingDim)
	}
	flags, err := marshalFlags(claim.Flags)
	if err != nil {
		return fmt.Errorf("postgres: upsert political claim %q: %w", claim.ID, err)
	}

	if err := s.queries.UpsertPoliticalClaim(ctx, db.UpsertPoliticalClaimParams{
		ID:             claim.ID,
		Content:        claim.Text,
		LiteralVerdict: string(claim.LiteralVerdict),
		Flags:          flags,
		SourceName:     claim.SourceName,
		SourceUrl:      claim.SourceURL,
		QuotedSpan:     claim.QuotedSpan,
		Outlet:         claim.Outlet,
		CheckedAt:      timestamptz(claim.CheckedAt),
		Embedding:      pgvector.NewHalfVector(claim.Embedding),
	}); err != nil {
		return fmt.Errorf("postgres: upsert political claim %q: %w", claim.ID, err)
	}
	return nil
}

// SearchPoliticalClaims returns the topK curated claims closest to query by
// cosine distance, nearest first.
func (s *Store) SearchPoliticalClaims(ctx context.Context, query []float32, topK int) ([]domain.PoliticalClaimMatch, error) {
	if topK <= 0 || topK > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: search political claims: topK %d out of range", topK)
	}
	if len(query) != domain.EmbeddingDim {
		return nil, fmt.Errorf("postgres: search political claims: query has %d dims, want %d", len(query), domain.EmbeddingDim)
	}

	rows, err := s.queries.SearchPoliticalClaims(ctx, db.SearchPoliticalClaimsParams{
		QueryEmbedding: pgvector.NewHalfVector(query),
		ResultLimit:    int32(topK),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: search political claims: %w", err)
	}

	matches := make([]domain.PoliticalClaimMatch, 0, len(rows))
	for _, r := range rows {
		matches = append(matches, domain.PoliticalClaimMatch{
			ID:             r.ID,
			Text:           r.Content,
			LiteralVerdict: domain.LiteralVerdict(r.LiteralVerdict),
			Flags:          unmarshalFlags(r.Flags),
			SourceName:     r.SourceName,
			SourceURL:      r.SourceUrl,
			QuotedSpan:     r.QuotedSpan,
			Outlet:         r.Outlet,
			CheckedAt:      r.CheckedAt.Time,
			// Cosine distance is in [0,2]; the float32 narrowing matches
			// domain.PoliticalClaimMatch and is plenty precise for ranking.
			Distance: float32(r.Distance),
		})
	}
	return matches, nil
}

// UpsertVotingRecord inserts or replaces one recorded scrutin position, keyed by
// (person, scrutin). The chamber and position are validated against the domain
// enums before insert so they cannot violate the column CHECK constraints.
func (s *Store) UpsertVotingRecord(ctx context.Context, record domain.VotingRecord) error {
	if !record.Chamber.Valid() {
		return fmt.Errorf("postgres: upsert voting record %q/%q: invalid chamber %q", record.PersonID, record.ScrutinID, record.Chamber)
	}
	if !record.Position.Valid() {
		return fmt.Errorf("postgres: upsert voting record %q/%q: invalid position %q", record.PersonID, record.ScrutinID, record.Position)
	}

	if err := s.queries.UpsertVotingRecord(ctx, db.UpsertVotingRecordParams{
		PersonID:   record.PersonID,
		PersonName: record.PersonName,
		Chamber:    string(record.Chamber),
		ScrutinID:  record.ScrutinID,
		BillTitle:  record.BillTitle,
		VotedOn:    pgtype.Date{Time: record.VotedOn, Valid: true},
		Position:   string(record.Position),
		SourceUrl:  record.SourceURL,
	}); err != nil {
		return fmt.Errorf("postgres: upsert voting record %q/%q: %w", record.PersonID, record.ScrutinID, err)
	}
	return nil
}

// LookupVotingRecords returns every recorded position for one person on one bill
// on one date, the exact lookup the voting source adapter answers.
func (s *Store) LookupVotingRecords(ctx context.Context, personID, billTitle string, votedOn time.Time) ([]domain.VotingRecord, error) {
	rows, err := s.queries.LookupVotingRecords(ctx, db.LookupVotingRecordsParams{
		PersonID:  personID,
		BillTitle: billTitle,
		VotedOn:   pgtype.Date{Time: votedOn, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: lookup voting records %q/%q: %w", personID, billTitle, err)
	}

	records := make([]domain.VotingRecord, 0, len(rows))
	for _, r := range rows {
		records = append(records, domain.VotingRecord{
			PersonID:   r.PersonID,
			PersonName: r.PersonName,
			Chamber:    domain.Chamber(r.Chamber),
			ScrutinID:  r.ScrutinID,
			BillTitle:  r.BillTitle,
			VotedOn:    r.VotedOn.Time,
			Position:   domain.VotePosition(r.Position),
			SourceURL:  r.SourceUrl,
		})
	}
	return records, nil
}

// timestamptz maps a Go time to a pgtype.Timestamptz, storing the zero value as
// SQL NULL (no publication date recorded yet).
func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

// marshalFlags converts domain flags to the text[] column value, validating each
// against the known set. A nil slice encodes as an empty array (the column is
// NOT NULL with a '{}' default).
func marshalFlags(flags []domain.ManipulationFlag) ([]string, error) {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		if !f.Valid() {
			return nil, fmt.Errorf("invalid manipulation flag %q", f)
		}
		out = append(out, string(f))
	}
	return out, nil
}

// unmarshalFlags converts a text[] column value back to domain flags. It never
// returns nil so consumers always receive a usable slice.
func unmarshalFlags(raw []string) []domain.ManipulationFlag {
	flags := make([]domain.ManipulationFlag, 0, len(raw))
	for _, f := range raw {
		flags = append(flags, domain.ManipulationFlag(f))
	}
	return flags
}
