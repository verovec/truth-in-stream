package seed

import (
	"context"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// recordingTVChannelStore records every upsert keyed by slug, mimicking the
// real store's slug-keyed idempotency so the seed's call shape is exercised
// without a database.
type recordingTVChannelStore struct {
	bySlug map[string]domain.TVChannel
	calls  int
}

func newRecordingTVChannelStore() *recordingTVChannelStore {
	return &recordingTVChannelStore{bySlug: map[string]domain.TVChannel{}}
}

func (s *recordingTVChannelStore) UpsertTVChannelBySlug(_ context.Context, c domain.TVChannel) (domain.TVChannel, error) {
	s.calls++
	if existing, ok := s.bySlug[c.Slug]; ok {
		// Mirror the SQL: refresh descriptive fields, preserve operator toggles.
		existing.Name = c.Name
		existing.SourceKind = c.SourceKind
		existing.SourceRef = c.SourceRef
		s.bySlug[c.Slug] = existing
		return existing, nil
	}
	c.ID = c.Slug // deterministic stand-in id
	s.bySlug[c.Slug] = c
	return c, nil
}

func TestTVChannelsRegistryIsWellFormed(t *testing.T) {
	t.Parallel()
	channels := TVChannels()
	if len(channels) != 10 {
		t.Fatalf("registry has %d channels, want 10", len(channels))
	}
	seen := map[string]bool{}
	for _, c := range channels {
		if c.Slug == "" || seen[c.Slug] {
			t.Fatalf("slug %q is blank or duplicated", c.Slug)
		}
		seen[c.Slug] = true
		if c.Name == "" {
			t.Fatalf("channel %q has no name", c.Slug)
		}
		if !c.SourceKind.Valid() {
			t.Fatalf("channel %q has invalid source kind %q", c.Slug, c.SourceKind)
		}
		if c.SourceRef == "" {
			t.Fatalf("channel %q has no source ref", c.Slug)
		}
		if c.Enabled {
			t.Fatalf("seed channel %q must be disabled", c.Slug)
		}
		if !c.ArchiveEnabled {
			t.Fatalf("seed channel %q should have archiving armed by default", c.Slug)
		}
	}
}

func TestInsertTVChannelsIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newRecordingTVChannelStore()

	n, err := InsertTVChannels(t.Context(), store)
	if err != nil {
		t.Fatalf("InsertTVChannels: %v", err)
	}
	if n != 10 {
		t.Fatalf("seeded %d channels, want 10", n)
	}
	if len(store.bySlug) != 10 {
		t.Fatalf("store holds %d channels, want 10", len(store.bySlug))
	}

	// Operator enables one channel, then a reseed runs. The reseed must not undo
	// the operator's change (the store preserves toggles) and must not add rows.
	enabled := store.bySlug["franceinfo"]
	enabled.Enabled = true
	store.bySlug["franceinfo"] = enabled

	if _, err := InsertTVChannels(t.Context(), store); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if len(store.bySlug) != 10 {
		t.Fatalf("reseed changed channel count to %d, want 10", len(store.bySlug))
	}
	if !store.bySlug["franceinfo"].Enabled {
		t.Fatalf("reseed clobbered the operator's enable toggle")
	}
}
