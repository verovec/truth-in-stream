package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/store"
)

// readingCache is a test AnalysisCache that serves a canned Get response so the
// reader's hit/miss/version/error behavior can be asserted without a real Redis.
// It also counts Get calls so a test can confirm the cache was (or was not)
// consulted.
type readingCache struct {
	mu      sync.Mutex
	gets    int
	payload []byte
	found   bool
	getErr  error
}

func (c *readingCache) Get(_ context.Context, _ string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	return append([]byte(nil), c.payload...), c.found, nil
}

func (c *readingCache) Put(context.Context, string, []byte, time.Duration) error {
	return nil
}

func (c *readingCache) getCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets
}

func TestNewSnapshotReaderValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewSnapshotReader(nil, nil); err == nil {
		t.Error("a nil cache must be rejected")
	}
	if _, err := NewSnapshotReader(store.NoopCache{}, nil); err != nil {
		t.Errorf("a valid reader must build: %v", err)
	}
}

func TestSnapshotReaderHitReturnsEvents(t *testing.T) {
	t.Parallel()
	events := sampleEvents()
	payload, err := MarshalSnapshot("vid1", events)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cache := &readingCache{payload: payload, found: true}
	reader, err := NewSnapshotReader(cache, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	got, found, err := reader.Snapshot(t.Context(), "vid1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !found {
		t.Fatal("found = false, want a hit")
	}
	if diff := cmp.Diff(events, got); diff != "" {
		t.Errorf("replayed events (-want +got):\n%s", diff)
	}
}

func TestSnapshotReaderMissReturnsNotFound(t *testing.T) {
	t.Parallel()
	// A clean cache miss is reported as not-found with no error, so the caller
	// falls through to the live pipeline.
	cache := &readingCache{found: false}
	reader, err := NewSnapshotReader(cache, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	got, found, err := reader.Snapshot(t.Context(), "vid1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if found {
		t.Fatal("found = true, want a miss")
	}
	if got != nil {
		t.Errorf("events = %v, want nil on a miss", got)
	}
	if cache.getCount() != 1 {
		t.Errorf("get calls = %d, want exactly 1", cache.getCount())
	}
}

func TestSnapshotReaderUnsupportedVersionIsMiss(t *testing.T) {
	t.Parallel()
	// A cached payload stamped with an unknown schema version is treated as a miss
	// (not-found, no error) so the caller re-runs the pipeline rather than failing.
	cache := &readingCache{payload: []byte(`{"version":999,"video_id":"vid1","events":[]}`), found: true}
	reader, err := NewSnapshotReader(cache, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	got, found, err := reader.Snapshot(t.Context(), "vid1")
	if err != nil {
		t.Fatalf("snapshot must not error on a version mismatch, got %v", err)
	}
	if found {
		t.Fatal("found = true, want a miss for an unsupported version")
	}
	if got != nil {
		t.Errorf("events = %v, want nil", got)
	}
}

func TestSnapshotReaderCorruptPayloadIsMiss(t *testing.T) {
	t.Parallel()
	// A corrupt (non-decodable) payload is treated as a miss so a damaged entry
	// degrades to the live pipeline rather than failing the session.
	cache := &readingCache{payload: []byte("not json"), found: true}
	reader, err := NewSnapshotReader(cache, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	got, found, err := reader.Snapshot(t.Context(), "vid1")
	if err != nil {
		t.Fatalf("snapshot must not error on a corrupt payload, got %v", err)
	}
	if found {
		t.Fatal("found = true, want a miss for a corrupt payload")
	}
	if got != nil {
		t.Errorf("events = %v, want nil", got)
	}
}

func TestSnapshotReaderEmptyEventsIsMiss(t *testing.T) {
	t.Parallel()
	// A well-formed, current-version snapshot that carries no events is nothing to
	// replay; it is treated as a miss so a content-free entry never displaces a real
	// run, mirroring the persister's refusal to write an empty analysis.
	cache := &readingCache{payload: []byte(`{"version":1,"video_id":"vid1","events":[]}`), found: true}
	reader, err := NewSnapshotReader(cache, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	got, found, err := reader.Snapshot(t.Context(), "vid1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if found {
		t.Fatal("found = true, want a miss for an empty snapshot")
	}
	if got != nil {
		t.Errorf("events = %v, want nil", got)
	}
}

func TestSnapshotReaderCacheErrorIsMiss(t *testing.T) {
	t.Parallel()
	// A backend error from the cache is logged and treated as a miss: a degraded
	// cache must fall through to the live pipeline, never fail the session.
	cache := &readingCache{getErr: errors.New("redis down")}
	reader, err := NewSnapshotReader(cache, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	got, found, err := reader.Snapshot(t.Context(), "vid1")
	if err != nil {
		t.Fatalf("snapshot must not surface a cache error, got %v", err)
	}
	if found {
		t.Fatal("found = true, want a miss on a cache error")
	}
	if got != nil {
		t.Errorf("events = %v, want nil", got)
	}
}

func TestSnapshotReaderRequiresVideoID(t *testing.T) {
	t.Parallel()
	// An empty video id is a miss without touching the cache: there is no key to
	// look up, and the caller falls through to the live pipeline.
	cache := &readingCache{found: true, payload: []byte("{}")}
	reader, err := NewSnapshotReader(cache, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	_, found, err := reader.Snapshot(t.Context(), "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if found {
		t.Fatal("found = true, want a miss for an empty video id")
	}
	if cache.getCount() != 0 {
		t.Errorf("get calls = %d, want 0 for an empty video id", cache.getCount())
	}
}

func TestSnapshotReaderNoopCacheIsAlwaysMiss(t *testing.T) {
	t.Parallel()
	// With caching disabled (the NoopCache), every lookup is a clean miss, so a
	// build without Redis runs the live pipeline exactly as before.
	reader, err := NewSnapshotReader(store.NoopCache{}, nil)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	_, found, err := reader.Snapshot(t.Context(), "vid1")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if found {
		t.Fatal("found = true, want a miss with the noop cache")
	}
}
