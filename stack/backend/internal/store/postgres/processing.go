package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// Store satisfies the processing results port.
var _ domain.SegmentResultStore = (*Store)(nil)

// SaveSegmentResult inserts or replaces the result keyed by
// (videoID, result.Start). Timestamps are persisted as milliseconds, the
// precision transcription segments carry.
func (s *Store) SaveSegmentResult(ctx context.Context, videoID string, result domain.SegmentResult) error {
	matches, err := marshalMatches(result.Matches)
	if err != nil {
		return fmt.Errorf("postgres: save segment %s at %s: %w", videoID, result.Start, err)
	}
	err = s.queries.UpsertSegmentResult(ctx, db.UpsertSegmentResultParams{
		VideoID: videoID,
		StartMs: result.Start.Milliseconds(),
		EndMs:   result.End.Milliseconds(),
		Content: result.Text,
		Matches: matches,
	})
	if err != nil {
		return fmt.Errorf("postgres: save segment %s at %s: %w", videoID, result.Start, err)
	}
	return nil
}

// MarkVideoProcessed records that all segmentCount segments of videoID have
// been persisted.
func (s *Store) MarkVideoProcessed(ctx context.Context, videoID string, segmentCount int) error {
	if segmentCount < 0 || segmentCount > math.MaxInt32 {
		return fmt.Errorf("postgres: mark processed %s: segment count %d out of range", videoID, segmentCount)
	}
	err := s.queries.MarkVideoProcessed(ctx, db.MarkVideoProcessedParams{
		VideoID:      videoID,
		SegmentCount: int32(segmentCount),
	})
	if err != nil {
		return fmt.Errorf("postgres: mark processed %s: %w", videoID, err)
	}
	return nil
}

// ProcessedSegmentCount returns the persisted segment count for videoID and
// whether the video has been fully processed.
func (s *Store) ProcessedSegmentCount(ctx context.Context, videoID string) (int, bool, error) {
	count, err := s.queries.GetProcessedVideoSegmentCount(ctx, videoID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("postgres: processed count %s: %w", videoID, err)
	}
	return int(count), true, nil
}

// ListSegmentResults returns every persisted result for videoID ordered by
// segment start time.
func (s *Store) ListSegmentResults(ctx context.Context, videoID string) ([]domain.SegmentResult, error) {
	rows, err := s.queries.ListSegmentResults(ctx, videoID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list results %s: %w", videoID, err)
	}

	results := make([]domain.SegmentResult, 0, len(rows))
	for _, r := range rows {
		matches, err := unmarshalMatches(r.Matches)
		if err != nil {
			return nil, fmt.Errorf("postgres: list results %s at %dms: %w", videoID, r.StartMs, err)
		}
		results = append(results, domain.SegmentResult{
			Segment: domain.Segment{
				Start: time.Duration(r.StartMs) * time.Millisecond,
				End:   time.Duration(r.EndMs) * time.Millisecond,
				Text:  r.Content,
			},
			Matches: matches,
		})
	}
	return results, nil
}

// marshalMatches encodes segment matches as a jsonb array, normalizing nil to
// an empty array so the NOT NULL column never receives SQL NULL.
func marshalMatches(matches []domain.SegmentMatch) ([]byte, error) {
	if matches == nil {
		matches = []domain.SegmentMatch{}
	}
	raw, err := json.Marshal(matches)
	if err != nil {
		return nil, fmt.Errorf("marshal matches: %w", err)
	}
	return raw, nil
}

// unmarshalMatches decodes a segment_results.matches jsonb value, defaulting
// to an empty slice so consumers never receive nil.
func unmarshalMatches(raw []byte) ([]domain.SegmentMatch, error) {
	matches := []domain.SegmentMatch{}
	if len(raw) == 0 {
		return matches, nil
	}
	if err := json.Unmarshal(raw, &matches); err != nil {
		return nil, fmt.Errorf("unmarshal matches: %w", err)
	}
	if matches == nil {
		matches = []domain.SegmentMatch{}
	}
	return matches, nil
}
