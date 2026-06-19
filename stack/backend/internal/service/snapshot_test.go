package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store"
)

// recordingCache is a test AnalysisCache that records the last Put and can be set
// to fail it, so the persister's serialization, key, TTL, and error handling can
// all be asserted without a real Redis.
type recordingCache struct {
	mu      sync.Mutex
	putErr  error
	puts    int
	videoID string
	payload []byte
	ttl     time.Duration
}

func (c *recordingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

func (c *recordingCache) Put(_ context.Context, videoID string, payload []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	if c.putErr != nil {
		return c.putErr
	}
	c.videoID = videoID
	c.payload = append([]byte(nil), payload...)
	c.ttl = ttl
	return nil
}

func (c *recordingCache) lastPut() (int, string, []byte, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.puts, c.videoID, append([]byte(nil), c.payload...), c.ttl
}

func sampleEvents() []LiveEvent {
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	cite := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "earth shape", Similarity: 0.7, EvidenceID: "evidence:1:0"}
	conf := domain.Confidence{Score: 0.8, Supporting: 1, EvidenceItems: 1}
	return []LiveEvent{
		{Kind: LiveEventInterim, Segment: domain.Segment{Text: "the earth"}},
		{Kind: LiveEventSubtitle, ID: "0", Segment: seg},
		{Kind: LiveEventClaims, ID: "0", Segment: seg, Claims: []AtomicClaim{{ClaimID: "0-0", Text: "the earth is round."}}},
		{
			Kind: LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: ClaimStatusVerified, Source: SourceVerified,
			Verdict: &VerifiedVerdict{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.9, Citations: []domain.SegmentMatch{cite}, Rationale: "round", Literal: LiteralAccurate, Flags: []string{FlagCherryPicked}},
		},
		{Kind: LiveEventResult, ID: "1", Segment: seg, Matches: []domain.SegmentMatch{cite}, Confidence: &conf},
		{Kind: LiveEventConsistency, ID: "2", Segment: seg, Consistency: &ConsistencyFlag{EarlierID: "0", EarlierText: "flat", Speaker: "A", Rationale: "contradiction"}},
		{Kind: LiveEventSpeakerScore, SpeakerScore: &SpeakerScore{Speaker: "A", Score: 0.6, Credible: 1, MisleadingFraming: 1}},
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	events := sampleEvents()

	payload, err := MarshalSnapshot("vid1", events)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalSnapshot(payload)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != SnapshotVersion {
		t.Errorf("version = %d, want %d", got.Version, SnapshotVersion)
	}
	if got.VideoID != "vid1" {
		t.Errorf("video id = %q, want vid1", got.VideoID)
	}
	if diff := cmp.Diff(events, got.Events); diff != "" {
		t.Errorf("events did not round-trip (-want +got):\n%s", diff)
	}
}

func TestUnmarshalSnapshotRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()
	// A payload stamped with a version this build does not understand is rejected
	// with ErrSnapshotVersion so the replay path treats it as a miss.
	payload := []byte(`{"version":999,"video_id":"vid1","events":[]}`)
	if _, err := UnmarshalSnapshot(payload); !errors.Is(err, ErrSnapshotVersion) {
		t.Fatalf("err = %v, want ErrSnapshotVersion", err)
	}
}

func TestUnmarshalSnapshotRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	if _, err := UnmarshalSnapshot([]byte("not json")); err == nil {
		t.Fatal("expected an error on malformed json")
	}
}

func TestNewSnapshotPersisterValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewSnapshotPersister(nil, time.Hour, nil); err == nil {
		t.Error("a nil cache must be rejected")
	}
	if _, err := NewSnapshotPersister(store.NoopCache{}, 0, nil); err == nil {
		t.Error("a non-positive ttl must be rejected")
	}
	if _, err := NewSnapshotPersister(store.NoopCache{}, time.Hour, nil); err != nil {
		t.Errorf("a valid persister must build: %v", err)
	}
}

func TestPersistWritesSnapshotUnderVideoIDWithTTL(t *testing.T) {
	t.Parallel()
	cache := &recordingCache{}
	persister, err := NewSnapshotPersister(cache, 24*time.Hour, nil)
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}

	events := sampleEvents()
	if err := persister.Persist(t.Context(), "vid1", events); err != nil {
		t.Fatalf("persist: %v", err)
	}

	puts, videoID, payload, ttl := cache.lastPut()
	if puts != 1 {
		t.Fatalf("puts = %d, want exactly 1", puts)
	}
	if videoID != "vid1" {
		t.Errorf("video id = %q, want vid1", videoID)
	}
	if ttl != 24*time.Hour {
		t.Errorf("ttl = %s, want 24h", ttl)
	}
	snapshot, err := UnmarshalSnapshot(payload)
	if err != nil {
		t.Fatalf("the persisted payload must round-trip: %v", err)
	}
	if diff := cmp.Diff(events, snapshot.Events); diff != "" {
		t.Errorf("persisted events (-want +got):\n%s", diff)
	}
}

func TestPersistSkipsEmptyAnalysis(t *testing.T) {
	t.Parallel()
	// A completion that produced no events is nothing to replay; it must not write
	// a cache entry that would mask a later real run.
	cache := &recordingCache{}
	persister, err := NewSnapshotPersister(cache, time.Hour, nil)
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}
	if err := persister.Persist(t.Context(), "vid1", nil); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if puts, _, _, _ := cache.lastPut(); puts != 0 {
		t.Errorf("puts = %d, want 0 for an empty analysis", puts)
	}
}

func TestPersistRequiresVideoID(t *testing.T) {
	t.Parallel()
	persister, err := NewSnapshotPersister(store.NoopCache{}, time.Hour, nil)
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}
	if err := persister.Persist(t.Context(), "", sampleEvents()); err == nil {
		t.Error("an empty video id must be rejected")
	}
}

func TestPersistPropagatesCacheError(t *testing.T) {
	t.Parallel()
	// A cache write failure is returned (wrapped) so the caller can log it; the
	// caller treats it as non-fatal, but Persist itself does not swallow it.
	sentinel := errors.New("redis down")
	cache := &recordingCache{putErr: sentinel}
	persister, err := NewSnapshotPersister(cache, time.Hour, nil)
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}
	err = persister.Persist(t.Context(), "vid1", sampleEvents())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap the cache error", err)
	}
}

func TestPersistNoopCacheIsCleanNoop(t *testing.T) {
	t.Parallel()
	// With caching disabled (the NoopCache), Persist is a clean no-op: it returns
	// nil and discards the payload, so a build without Redis behaves as before.
	persister, err := NewSnapshotPersister(store.NoopCache{}, time.Hour, nil)
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}
	if err := persister.Persist(t.Context(), "vid1", sampleEvents()); err != nil {
		t.Errorf("noop persist must succeed cleanly, got %v", err)
	}
}
