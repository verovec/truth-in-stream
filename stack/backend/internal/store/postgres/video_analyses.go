package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// Store satisfies the durable video analysis port.
var _ domain.VideoAnalysisStore = (*Store)(nil)

// StartVideoAnalysis claims a ready video for a fresh analysis run: it flips
// the video to analysing (the job lock), zeroes the progress position, and
// clears any prior error in one guarded update. It returns the locked record,
// or a classified error when the guard admits no row: domain.ErrVideoNotFound
// (unknown), domain.ErrVideoAnalysisInProgress (already analysing), or
// domain.ErrVideoNotReady (upload not ready). The previous stored analysis is
// deliberately not wiped: it stays readable until the new run completes and
// overwrites it.
func (s *Store) StartVideoAnalysis(ctx context.Context, id string) (domain.Video, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	row, err := s.queries.LockVideoForAnalysis(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		// The guard matched no row: resolve why for the caller. The upload
		// status decides not-ready; every other refusal is a concurrency
		// conflict - the usual case is a run still analysing, and the rare
		// race where that run completed between the guard and this read is
		// still "another run got there first", so it classifies the same way
		// rather than as a false not-ready.
		existing, getErr := s.queries.GetVideo(ctx, uid)
		if errors.Is(getErr, pgx.ErrNoRows) {
			return domain.Video{}, domain.ErrVideoNotFound
		}
		if getErr != nil {
			return domain.Video{}, fmt.Errorf("postgres: start video analysis %s: resolve conflict: %w", id, getErr)
		}
		if existing.Status != string(domain.VideoStatusReady) {
			return domain.Video{}, domain.ErrVideoNotReady
		}
		return domain.Video{}, domain.ErrVideoAnalysisInProgress
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: start video analysis %s: %w", id, err)
	}
	return videoFromRow(row), nil
}

// SetVideoAnalysisProgress records the audio position the running analysis has
// reached, so progress survives a refresh and a restart.
func (s *Store) SetVideoAnalysisProgress(ctx context.Context, id string, progressMS int64) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrVideoNotFound
	}
	if err := s.queries.SetVideoAnalysisProgress(ctx, db.SetVideoAnalysisProgressParams{ID: uid, AnalysisProgressMs: progressMS}); err != nil {
		return fmt.Errorf("postgres: set video analysis progress %s: %w", id, err)
	}
	return nil
}

// CompleteVideoAnalysis atomically stores a completed run and flips the
// video's lifecycle to complete in one transaction: the status flip stamps
// analyzed_at and counts the run, and the upsert overwrites the single
// per-video analysis row. A failure of either leaves both untouched, so the
// lifecycle can never claim a result that is not stored (nor the reverse). An
// unknown video maps to domain.ErrVideoNotFound.
func (s *Store) CompleteVideoAnalysis(ctx context.Context, a domain.VideoAnalysis) (domain.VideoAnalysis, error) {
	uid, err := uuid.Parse(a.VideoID)
	if err != nil {
		return domain.VideoAnalysis{}, domain.ErrVideoNotFound
	}
	if len(a.Events) == 0 {
		return domain.VideoAnalysis{}, fmt.Errorf("postgres: complete video analysis %s: events payload is required", a.VideoID)
	}
	engine := a.Engine
	if len(engine) == 0 {
		engine = []byte("{}")
	}

	var stored db.VideoAnalysis
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if _, err := q.CompleteVideoAnalysisStatus(ctx, uid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrVideoNotFound
			}
			return fmt.Errorf("flip status: %w", err)
		}
		row, err := q.UpsertVideoAnalysis(ctx, db.UpsertVideoAnalysisParams{
			VideoID:            uid,
			SnapshotVersion:    int32(a.SnapshotVersion),
			Events:             a.Events,
			Engine:             engine,
			ClaimsTotal:        int32(a.ClaimsTotal),
			ClaimsCredible:     int32(a.ClaimsCredible),
			ClaimsDisputed:     int32(a.ClaimsDisputed),
			ClaimsUnverifiable: int32(a.ClaimsUnverifiable),
		})
		if err != nil {
			return fmt.Errorf("upsert analysis: %w", err)
		}
		stored = row
		return nil
	})
	if errors.Is(err, domain.ErrVideoNotFound) {
		return domain.VideoAnalysis{}, domain.ErrVideoNotFound
	}
	if err != nil {
		return domain.VideoAnalysis{}, fmt.Errorf("postgres: complete video analysis %s: %w", a.VideoID, err)
	}
	return videoAnalysisFromRow(stored), nil
}

// FailVideoAnalysis records the reason a run failed and flips it to failed so
// the operator can re-analyze. A previously stored analysis is untouched.
func (s *Store) FailVideoAnalysis(ctx context.Context, id, reason string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrVideoNotFound
	}
	if err := s.queries.FailVideoAnalysis(ctx, db.FailVideoAnalysisParams{ID: uid, AnalysisError: reason}); err != nil {
		return fmt.Errorf("postgres: fail video analysis %s: %w", id, err)
	}
	return nil
}

// RecoverInterruptedVideoAnalyses flips every video left analysing (the
// process died mid-run) to failed with a clear reason, returning the
// recovered ids for startup logging.
func (s *Store) RecoverInterruptedVideoAnalyses(ctx context.Context) ([]string, error) {
	ids, err := s.queries.RecoverInterruptedVideoAnalyses(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: recover interrupted video analyses: %w", err)
	}
	recovered := make([]string, 0, len(ids))
	for _, id := range ids {
		recovered = append(recovered, id.String())
	}
	return recovered, nil
}

// GetVideoAnalysis returns the stored analysis for the video. An unparseable
// id, like a missing row, maps to domain.ErrVideoAnalysisNotFound: neither can
// name a stored analysis.
func (s *Store) GetVideoAnalysis(ctx context.Context, videoID string) (domain.VideoAnalysis, error) {
	uid, err := uuid.Parse(videoID)
	if err != nil {
		return domain.VideoAnalysis{}, domain.ErrVideoAnalysisNotFound
	}
	row, err := s.queries.GetVideoAnalysis(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VideoAnalysis{}, domain.ErrVideoAnalysisNotFound
	}
	if err != nil {
		return domain.VideoAnalysis{}, fmt.Errorf("postgres: get video analysis %s: %w", videoID, err)
	}
	return videoAnalysisFromRow(row), nil
}

// videoAnalysisFromRow maps a generated row to the domain type. Events and
// Engine pass through as opaque JSON.
func videoAnalysisFromRow(r db.VideoAnalysis) domain.VideoAnalysis {
	return domain.VideoAnalysis{
		VideoID:            r.VideoID.String(),
		SnapshotVersion:    int(r.SnapshotVersion),
		Events:             r.Events,
		Engine:             r.Engine,
		ClaimsTotal:        int(r.ClaimsTotal),
		ClaimsCredible:     int(r.ClaimsCredible),
		ClaimsDisputed:     int(r.ClaimsDisputed),
		ClaimsUnverifiable: int(r.ClaimsUnverifiable),
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
	}
}
