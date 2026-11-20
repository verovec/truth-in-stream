package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// InsertClaimChecks appends telemetry rows in one batch. The table is
// append-only analytics: no conflict handling, no read-back.
func (s *Store) InsertClaimChecks(ctx context.Context, checks []domain.ClaimCheck) error {
	if len(checks) == 0 {
		return nil
	}
	params := make([]db.InsertClaimChecksParams, len(checks))
	for i, c := range checks {
		occurred := c.OccurredAt
		if occurred.IsZero() {
			occurred = time.Now()
		}
		params[i] = db.InsertClaimChecksParams{
			OccurredAt:            pgtype.Timestamptz{Time: occurred, Valid: true},
			SessionKind:           c.SessionKind,
			Locale:                c.Locale,
			Speaker:               c.Speaker,
			UnitText:              c.UnitText,
			ClaimText:             c.ClaimText,
			DecisionPath:          c.DecisionPath,
			SkipReason:            c.SkipReason,
			RetrievalTop:          c.RetrievalTop,
			RetrievalCandidates:   int32(c.RetrievalCandidates),
			RetrievalClaimHits:    int32(c.RetrievalClaimHits),
			RetrievalEvidenceHits: int32(c.RetrievalEvidenceHits),
			Verdict:               c.Verdict,
			Basis:                 c.Basis,
			Literal:               c.Literal,
			Confidence:            c.Confidence,
			Source:                c.Source,
			Escalated:             c.Escalated,
			LlmCalls:              int32(c.LLMCalls),
			LatencyMs:             c.LatencyMS,
		}
	}
	if err := firstBatchError(s.queries.InsertClaimChecks(ctx, params)); err != nil {
		return fmt.Errorf("postgres: insert claim checks: %w", err)
	}
	return nil
}

// CountClaimChecksBefore reports how many telemetry rows an apply of the
// retention sweep would remove: the dry-run read.
func (s *Store) CountClaimChecksBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := s.queries.CountClaimChecksBefore(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("postgres: count claim checks before %s: %w", cutoff, err)
	}
	return n, nil
}

// DeleteClaimChecksBefore ages out telemetry rows older than cutoff and
// returns how many were removed.
func (s *Store) DeleteClaimChecksBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := s.queries.DeleteClaimChecksBefore(ctx, pgtype.Timestamptz{Time: cutoff, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("postgres: delete claim checks before %s: %w", cutoff, err)
	}
	return n, nil
}

// ListClaimChecksSince reads telemetry rows recorded at or after since,
// oldest first: the dataset-build and test read path.
func (s *Store) ListClaimChecksSince(ctx context.Context, since time.Time) ([]domain.ClaimCheck, error) {
	rows, err := s.queries.ListClaimChecksSince(ctx, pgtype.Timestamptz{Time: since, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("postgres: list claim checks: %w", err)
	}
	checks := make([]domain.ClaimCheck, len(rows))
	for i, r := range rows {
		checks[i] = domain.ClaimCheck{
			OccurredAt:            r.OccurredAt.Time,
			SessionKind:           r.SessionKind,
			Locale:                r.Locale,
			Speaker:               r.Speaker,
			UnitText:              r.UnitText,
			ClaimText:             r.ClaimText,
			DecisionPath:          r.DecisionPath,
			SkipReason:            r.SkipReason,
			RetrievalTop:          r.RetrievalTop,
			RetrievalCandidates:   int(r.RetrievalCandidates),
			RetrievalClaimHits:    int(r.RetrievalClaimHits),
			RetrievalEvidenceHits: int(r.RetrievalEvidenceHits),
			Verdict:               r.Verdict,
			Basis:                 r.Basis,
			Literal:               r.Literal,
			Confidence:            r.Confidence,
			Source:                r.Source,
			Escalated:             r.Escalated,
			LLMCalls:              int(r.LlmCalls),
			LatencyMS:             r.LatencyMs,
		}
	}
	return checks, nil
}
