package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// StoredAnalysisPersister is the durable write side of a video's completed
// pre-analysis: it owns the event-stream encoding, the snapshot version stamp,
// and the derived claim counters, then hands the assembled record to the store
// for the atomic upsert-and-complete write. It is the Postgres mirror of the
// Redis SnapshotPersister, consumed by the headless pre-analysis job; the live
// view's recorder stays Redis-only, so a lossy browser session can never
// overwrite a deliberate pre-analysis.
type StoredAnalysisPersister struct {
	store domain.VideoAnalysisStore
}

// NewStoredAnalysisPersister wires a persister over the durable analysis store.
func NewStoredAnalysisPersister(store domain.VideoAnalysisStore) (*StoredAnalysisPersister, error) {
	if store == nil {
		return nil, fmt.Errorf("service: stored analysis persister: store is required")
	}
	return &StoredAnalysisPersister{store: store}, nil
}

// Persist durably stores a completed pre-analysis and flips the video's
// lifecycle to complete, returning the stored record. engine is the run's
// model-and-config fingerprint as JSON; empty defaults to an empty object.
// Unlike the Redis persister, an empty event list is an error, not a silent
// skip: a deliberate pre-analysis that produced nothing is a failed run the
// job must record as such, never a content-free completion.
func (p *StoredAnalysisPersister) Persist(ctx context.Context, videoID string, events []LiveEvent, engine json.RawMessage) (domain.VideoAnalysis, error) {
	if videoID == "" {
		return domain.VideoAnalysis{}, fmt.Errorf("service: persisting stored analysis: video id is required")
	}
	if len(events) == 0 {
		return domain.VideoAnalysis{}, fmt.Errorf("service: persisting stored analysis for %q: no events to persist", videoID)
	}
	if len(engine) == 0 {
		engine = json.RawMessage("{}")
	} else if !json.Valid(engine) {
		return domain.VideoAnalysis{}, fmt.Errorf("service: persisting stored analysis for %q: engine is not valid JSON", videoID)
	}
	payload, err := json.Marshal(events)
	if err != nil {
		return domain.VideoAnalysis{}, fmt.Errorf("service: marshaling stored analysis events for %q: %w", videoID, err)
	}
	total, credible, disputed, unverifiable := countClaimVerdicts(events)
	stored, err := p.store.CompleteVideoAnalysis(ctx, domain.VideoAnalysis{
		VideoID:            videoID,
		SnapshotVersion:    SnapshotVersion,
		Events:             payload,
		Engine:             []byte(engine),
		ClaimsTotal:        total,
		ClaimsCredible:     credible,
		ClaimsDisputed:     disputed,
		ClaimsUnverifiable: unverifiable,
	})
	if err != nil {
		return domain.VideoAnalysis{}, fmt.Errorf("service: persisting stored analysis for %q: %w", videoID, err)
	}
	return stored, nil
}

// countClaimVerdicts derives the denormalized claim counters from an event
// stream. A claim is counted once: total covers every announced or resolved
// atomic claim, and the verdict buckets take each claim's final verdict - a
// later result for the same claim id (a terminal-gate upgrade) replaces the
// earlier one, so an upgraded claim moves buckets instead of counting twice. A
// claim that never reached a verdict (unchecked, error) counts in the total
// only.
func countClaimVerdicts(events []LiveEvent) (total, credible, disputed, unverifiable int) {
	seen := make(map[string]struct{})
	verdicts := make(map[string]string)
	for _, ev := range events {
		switch ev.Kind {
		case LiveEventClaims:
			for _, c := range ev.Claims {
				seen[c.ClaimID] = struct{}{}
			}
		case LiveEventResult:
			if ev.ClaimID == "" {
				continue
			}
			seen[ev.ClaimID] = struct{}{}
			if ev.Verdict != nil {
				verdicts[ev.ClaimID] = ev.Verdict.Verdict
			}
		}
	}
	for _, v := range verdicts {
		switch v {
		case VerdictCredible:
			credible++
		case VerdictDisputed:
			disputed++
		case VerdictUnverifiable:
			unverifiable++
		}
	}
	return len(seen), credible, disputed, unverifiable
}

// videoRecordSource is the slice of the video store the analysis read side
// needs: the lifecycle fields on the video record. Satisfied by
// *postgres.Store (and any domain.VideoStore).
type videoRecordSource interface {
	GetVideo(ctx context.Context, id string) (domain.Video, error)
}

// VideoAnalysisView is a video's durable analysis as the read API serves it:
// the video record carries the lifecycle (status, error, progress, runs,
// analyzed_at), Analysis carries the stored result's engine and counters, and
// Events is the decoded stream the handler reshapes to wire frames. Analysis
// and Events are set only when the lifecycle is complete; Events is
// additionally nil when the stored payload cannot be decoded by this build (a
// snapshot-version bump), leaving the lifecycle honest while the operator
// re-analyses.
type VideoAnalysisView struct {
	Video    domain.Video
	Analysis *domain.VideoAnalysis
	Events   []LiveEvent
}

// StoredAnalysisReader is the durable read side of a video's pre-analysis: it
// serves the replay path (Snapshot, the Postgres source of the composite
// replayer) and the read API (Get). It owns the snapshot-version check and the
// event decoding, mirroring the Redis SnapshotReader's degradation contract:
// on the replay path every unreadable outcome collapses to a miss with a nil
// error, because the next source or the live pipeline is always a correct
// fallback.
type StoredAnalysisReader struct {
	videos   videoRecordSource
	analyses domain.VideoAnalysisStore
	logger   *slog.Logger
}

// NewStoredAnalysisReader wires a reader over the video record and durable
// analysis stores.
func NewStoredAnalysisReader(videos videoRecordSource, analyses domain.VideoAnalysisStore, logger *slog.Logger) (*StoredAnalysisReader, error) {
	if videos == nil {
		return nil, fmt.Errorf("service: stored analysis reader: video store is required")
	}
	if analyses == nil {
		return nil, fmt.Errorf("service: stored analysis reader: analysis store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &StoredAnalysisReader{videos: videos, analyses: analyses, logger: logger}, nil
}

// Snapshot looks up the durably stored analysis for videoID and returns its
// ordered events on a valid hit. It reads the stored row directly, not the
// video's lifecycle status, so during a re-analysis (and after a failed one)
// the previous completed result keeps replaying until it is overwritten. A
// missing row, an unsupported snapshot version, a corrupt or empty payload,
// and a backend error all report found=false with a nil error, so the caller
// falls through to the next source in every degraded case.
func (r *StoredAnalysisReader) Snapshot(ctx context.Context, videoID string) ([]LiveEvent, bool, error) {
	if videoID == "" {
		return nil, false, nil
	}
	analysis, err := r.analyses.GetVideoAnalysis(ctx, videoID)
	if errors.Is(err, domain.ErrVideoAnalysisNotFound) {
		return nil, false, nil
	}
	if err != nil {
		r.logger.WarnContext(ctx, "stored analysis lookup failed, falling back",
			slog.String("video_id", videoID), slog.Any("err", err))
		return nil, false, nil
	}
	events, ok := r.decode(ctx, videoID, analysis)
	if !ok {
		return nil, false, nil
	}
	return events, true, nil
}

// Get returns the video's analysis lifecycle plus, when the latest run
// completed, the stored result and its decoded events. An unknown video is
// domain.ErrVideoNotFound. A completed lifecycle whose stored row is missing
// or unreadable still reports its lifecycle honestly - with no result
// attached - rather than failing the read: the write is transactional, so
// this only occurs on a backend fault or a snapshot-version bump, and the
// operator's recovery is a re-analysis either way.
func (r *StoredAnalysisReader) Get(ctx context.Context, videoID string) (VideoAnalysisView, error) {
	video, err := r.videos.GetVideo(ctx, videoID)
	if err != nil {
		return VideoAnalysisView{}, fmt.Errorf("service: stored analysis view %q: %w", videoID, err)
	}
	view := VideoAnalysisView{Video: video}
	if video.AnalysisStatus != domain.VideoAnalysisComplete {
		return view, nil
	}
	analysis, err := r.analyses.GetVideoAnalysis(ctx, videoID)
	if err != nil {
		r.logger.WarnContext(ctx, "completed video has no readable stored analysis",
			slog.String("video_id", videoID), slog.Any("err", err))
		return view, nil
	}
	view.Analysis = &analysis
	if events, ok := r.decode(ctx, videoID, analysis); ok {
		view.Events = events
	}
	return view, nil
}

// decode validates the stored payload's snapshot version and unmarshals its
// events. Any unreadable outcome - a version this build does not write, a
// corrupt payload, or an empty event list (which the persister never writes) -
// is reported false and logged, never returned as an error: the callers'
// contract is to degrade, not fail.
func (r *StoredAnalysisReader) decode(ctx context.Context, videoID string, a domain.VideoAnalysis) ([]LiveEvent, bool) {
	if a.SnapshotVersion != SnapshotVersion {
		r.logger.WarnContext(ctx, "stored analysis snapshot version is unsupported",
			slog.String("video_id", videoID), slog.Int("version", a.SnapshotVersion), slog.Int("supported", SnapshotVersion))
		return nil, false
	}
	var events []LiveEvent
	if err := json.Unmarshal(a.Events, &events); err != nil {
		r.logger.WarnContext(ctx, "stored analysis events are unreadable",
			slog.String("video_id", videoID), slog.Any("err", err))
		return nil, false
	}
	if len(events) == 0 {
		r.logger.WarnContext(ctx, "stored analysis has no events",
			slog.String("video_id", videoID))
		return nil, false
	}
	return events, true
}
