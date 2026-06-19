package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/store"
)

// SnapshotVersion is the schema version of a persisted analysis snapshot. It is
// stamped on every snapshot and read back on replay so a future change to the
// event shape can bump it and reject (or migrate) an older payload rather than
// mis-decoding it. It is independent of the cache key's "v1" namespace: the key
// prefix partitions Redis entries, while this field versions the JSON value.
const SnapshotVersion = 1

// AnalysisSnapshot is the persisted record of a finite video's completed
// analysis: every LiveEvent the pipeline emitted, in order, under the video's id.
// It is the unit the cache-hit replay path re-emits to a client without re-running
// transcription or the LLMs. Events holds the same LiveEvent values the live
// socket streamed, so a replay reproduces the live session byte-for-byte at the
// service boundary; the handler reshapes them to wire frames exactly as it does
// live. The type is JSON-serializable as a single value (no streaming framing) so
// the whole snapshot is one cache entry.
type AnalysisSnapshot struct {
	Version int         `json:"version"`
	VideoID string      `json:"video_id"`
	Events  []LiveEvent `json:"events"`
}

// MarshalSnapshot serializes a completed analysis as a single JSON value for the
// byte-oriented cache. videoID is stamped into the snapshot so a replay can
// confirm the payload matches the key it was fetched under, and the schema
// version is set from SnapshotVersion.
func MarshalSnapshot(videoID string, events []LiveEvent) ([]byte, error) {
	payload, err := json.Marshal(AnalysisSnapshot{
		Version: SnapshotVersion,
		VideoID: videoID,
		Events:  events,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling analysis snapshot for %q: %w", videoID, err)
	}
	return payload, nil
}

// UnmarshalSnapshot decodes a cached payload back into an AnalysisSnapshot. It
// rejects a payload whose schema version is not the one this build writes, so a
// stale or forward-incompatible entry is treated as a miss by the caller rather
// than replayed as if it were current.
func UnmarshalSnapshot(payload []byte) (AnalysisSnapshot, error) {
	var snapshot AnalysisSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return AnalysisSnapshot{}, fmt.Errorf("unmarshaling analysis snapshot: %w", err)
	}
	if snapshot.Version != SnapshotVersion {
		return AnalysisSnapshot{}, fmt.Errorf("analysis snapshot version %d is not the supported %d: %w", snapshot.Version, SnapshotVersion, ErrSnapshotVersion)
	}
	return snapshot, nil
}

// ErrSnapshotVersion reports a cached snapshot whose schema version this build
// does not understand. The replay path matches it with errors.Is to treat the
// entry as a miss and re-run the pipeline rather than fail.
var ErrSnapshotVersion = errors.New("unsupported analysis snapshot version")

// SnapshotPersister captures a finite video's completed analysis to the cache.
// It owns the snapshot's serialization and TTL, keeping the handler free of both
// the wire format and the cache contract: the handler tees the live events and,
// on a genuine completion, hands the ordered events here. The cache is the
// byte-oriented store.AnalysisCache from the foundation card; a NoopCache makes
// Persist a clean no-op so a build with caching disabled behaves exactly as
// before.
type SnapshotPersister struct {
	cache  store.AnalysisCache
	ttl    time.Duration
	logger *slog.Logger
}

// NewSnapshotPersister wires a persister over a cache with the configured TTL.
// The TTL is the bounded replay window (ANALYSIS_CACHE_TTL, default 24h); it must
// be positive, matching the cache's own rejection of a non-positive expiry.
func NewSnapshotPersister(cache store.AnalysisCache, ttl time.Duration, logger *slog.Logger) (*SnapshotPersister, error) {
	if cache == nil {
		return nil, fmt.Errorf("service: snapshot persister: cache is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("service: snapshot persister: ttl must be positive, got %s", ttl)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotPersister{cache: cache, ttl: ttl, logger: logger}, nil
}

// Persist serializes the completed analysis and writes it to the cache under the
// video's id with the configured TTL. It is called only on a genuine completion
// (the handler's job to decide), never on an early disconnect. An empty event
// list is not persisted: a completion that produced no events is nothing worth
// replaying, and writing it would only mask a later real run. The write never
// fails the caller's live session - it is invoked after the stream has ended -
// and any cache error is wrapped and returned for the caller to log, not act on.
func (p *SnapshotPersister) Persist(ctx context.Context, videoID string, events []LiveEvent) error {
	if videoID == "" {
		return fmt.Errorf("service: persisting analysis snapshot: video id is required")
	}
	if len(events) == 0 {
		return nil
	}
	payload, err := MarshalSnapshot(videoID, events)
	if err != nil {
		return err
	}
	if err := p.cache.Put(ctx, videoID, payload, p.ttl); err != nil {
		return fmt.Errorf("persisting analysis snapshot for %q: %w", videoID, err)
	}
	return nil
}
