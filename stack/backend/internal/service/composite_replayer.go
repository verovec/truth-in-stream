package service

import (
	"context"
	"fmt"
	"log/slog"
)

// SnapshotSource is one prioritized source of a video's completed analysis:
// the durable Postgres store or the 24 h Redis cache. Satisfied by
// *StoredAnalysisReader and *SnapshotReader. found is false on every degraded
// outcome; an error is reserved for a fault the composite should log before
// moving on.
type SnapshotSource interface {
	Snapshot(ctx context.Context, videoID string) (events []LiveEvent, found bool, err error)
}

// CompositeReplayer serves a video's completed analysis from the first source
// that has it, in the order the sources were wired: Postgres first (a
// deliberate pre-analysis is permanent and authoritative), then Redis (a
// live-view replay within its TTL), then a miss that sends the caller to the
// live pipeline. It satisfies the handler's AnalysisReplayer port, so the live
// socket and the export endpoints gain the durable tier with no change of
// their own.
type CompositeReplayer struct {
	sources []SnapshotSource
	logger  *slog.Logger
}

// NewCompositeReplayer wires a replayer over the given sources, consulted in
// argument order.
func NewCompositeReplayer(logger *slog.Logger, sources ...SnapshotSource) (*CompositeReplayer, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("service: composite replayer: at least one source is required")
	}
	for i, src := range sources {
		if src == nil {
			return nil, fmt.Errorf("service: composite replayer: source %d is nil", i)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CompositeReplayer{sources: sources, logger: logger}, nil
}

// Snapshot returns the first source's hit, falling through on a miss. A
// source error is logged and treated as that source's miss - the next tier or
// the live pipeline is always a correct fallback - so a degraded store never
// fails a session.
func (c *CompositeReplayer) Snapshot(ctx context.Context, videoID string) ([]LiveEvent, bool, error) {
	for _, src := range c.sources {
		events, found, err := src.Snapshot(ctx, videoID)
		if err != nil {
			c.logger.WarnContext(ctx, "analysis snapshot source failed, trying next",
				slog.String("video_id", videoID), slog.Any("err", err))
			continue
		}
		if found {
			return events, true, nil
		}
	}
	return nil, false, nil
}
