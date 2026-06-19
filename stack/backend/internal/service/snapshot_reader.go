package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/store"
)

// SnapshotReader is the read side of the analysis cache: it fetches a finite
// video's persisted snapshot and decodes it back into the ordered LiveEvents the
// pipeline emitted, so the handler can replay a completed session without
// re-running transcription or the LLMs. It is the mirror of SnapshotPersister and
// owns the same two concerns the handler must not - the cache contract and the
// snapshot wire format - keeping the handler limited to HTTP and serialization.
//
// Every degraded outcome (a clean miss, an unsupported schema version, a corrupt
// payload, or a backend error) collapses to a single not-found result with a nil
// error, so the caller has exactly one fall-through to the live pipeline. An
// actual fault (a decode failure or a cache backend error) is logged here rather
// than surfaced, because a degraded cache must never fail a session: the live
// path is always a correct fallback.
type SnapshotReader struct {
	cache  store.AnalysisCache
	logger *slog.Logger
}

// NewSnapshotReader wires a reader over a cache. A NoopCache makes every lookup a
// clean miss, so a build with caching disabled runs the live pipeline unchanged.
func NewSnapshotReader(cache store.AnalysisCache, logger *slog.Logger) (*SnapshotReader, error) {
	if cache == nil {
		return nil, fmt.Errorf("service: snapshot reader: cache is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotReader{cache: cache, logger: logger}, nil
}

// Snapshot looks up the completed analysis for videoID and returns its ordered
// events on a valid hit. It reports found=false (with a nil error) for a miss, a
// disabled cache, an unsupported schema version, a corrupt payload, or a cache
// backend error, so the caller falls through to the live pipeline in every
// degraded case. A fault is logged, never returned: the live path is the correct
// fallback and a degraded cache must not fail the session.
func (r *SnapshotReader) Snapshot(ctx context.Context, videoID string) ([]LiveEvent, bool, error) {
	if videoID == "" {
		return nil, false, nil
	}
	payload, found, err := r.cache.Get(ctx, videoID)
	if err != nil {
		// The error can embed the connection target, so it is summarized to its type
		// rather than logged verbatim - never leak any part of the cache URL, which
		// may carry a password.
		r.logger.WarnContext(ctx, "analysis cache get failed, falling back to live pipeline",
			slog.String("video_id", videoID), slog.String("err_type", fmt.Sprintf("%T", err)))
		return nil, false, nil
	}
	if !found {
		return nil, false, nil
	}
	snapshot, err := UnmarshalSnapshot(payload)
	if err != nil {
		// A version mismatch or a corrupt entry is a miss, not a failure: re-run the
		// pipeline rather than serve a payload this build cannot replay faithfully.
		r.logger.WarnContext(ctx, "cached analysis snapshot is unreadable, falling back to live pipeline",
			slog.String("video_id", videoID), slog.Any("err", err))
		return nil, false, nil
	}
	// An entry with no events is nothing worth replaying - the persister never
	// writes one, so this is a damaged or hand-edited entry. Treat it as a miss so a
	// content-free replay never displaces a real run, mirroring the persister's own
	// empty-analysis rule.
	if len(snapshot.Events) == 0 {
		r.logger.WarnContext(ctx, "cached analysis snapshot has no events, falling back to live pipeline",
			slog.String("video_id", videoID))
		return nil, false, nil
	}
	return snapshot.Events, true, nil
}
