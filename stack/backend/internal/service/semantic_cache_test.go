package service

import (
	"testing"
	"time"
)

// The semantic cache keys verdicts on the claim's query embedding and replays a
// cached verdict when a new claim's embedding is within the similarity bar. These
// tables exercise the two behaviors the card's acceptance criteria name: a
// paraphrase (near-identical embedding) hits, and a genuinely different claim
// does not false-share.

func TestSemanticCacheParaphraseHits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		key   []float32
		query []float32
	}{
		{"identical embedding", []float32{1, 0, 0}, []float32{1, 0, 0}},
		{"near paraphrase clears the bar", []float32{1, 0, 0}, []float32{0.99, 0.1, 0}},
		{"scaled duplicate (cosine ignores magnitude)", []float32{0.6, 0.8, 0}, []float32{6, 8, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newSemanticCache(time.Minute, 0.95, 16)
			c.put(cacheEntry{source: SourceVerified, verdict: &VerifiedVerdict{Verdict: VerdictCredible}, embedding: tc.key})
			got, ok := c.get(tc.query)
			if !ok {
				t.Fatalf("get(%v) missed, want a paraphrase hit against %v", tc.query, tc.key)
			}
			if got.verdict == nil || got.verdict.Verdict != VerdictCredible {
				t.Errorf("hit verdict = %+v, want the cached credible verdict", got.verdict)
			}
		})
	}
}

func TestSemanticCacheFalseShareGuarded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		key   []float32
		query []float32
	}{
		{"orthogonal claim", []float32{1, 0, 0}, []float32{0, 1, 0}},
		{"opposed claim", []float32{1, 0, 0}, []float32{-1, 0, 0}},
		{"below the bar", []float32{1, 0, 0}, []float32{0.7, 0.7, 0}},
		{"empty query never matches", []float32{1, 0, 0}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newSemanticCache(time.Minute, 0.95, 16)
			c.put(cacheEntry{source: SourceVerified, verdict: &VerifiedVerdict{Verdict: VerdictCredible}, embedding: tc.key})
			if _, ok := c.get(tc.query); ok {
				t.Errorf("get(%v) hit, want a miss (a different claim must not share a verdict)", tc.query)
			}
		})
	}
}

func TestSemanticCacheReturnsNearestAboveBar(t *testing.T) {
	t.Parallel()
	// With several cached claims above the bar, the lookup returns the nearest one,
	// not merely the first, so the replayed verdict is the best available match.
	c := newSemanticCache(time.Minute, 0.9, 16)
	c.put(cacheEntry{source: SourceVerified, verdict: &VerifiedVerdict{Rationale: "far"}, embedding: []float32{0.95, 0.31, 0}})
	c.put(cacheEntry{source: SourceVerified, verdict: &VerifiedVerdict{Rationale: "near"}, embedding: []float32{1, 0, 0}})
	got, ok := c.get([]float32{1, 0, 0})
	if !ok {
		t.Fatal("get missed, want the nearest cached claim")
	}
	if got.verdict.Rationale != "near" {
		t.Errorf("hit = %q, want the nearest (%q)", got.verdict.Rationale, "near")
	}
}

func TestSemanticCacheExpiresEntries(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	c := newSemanticCache(time.Minute, 0.9, 16)
	c.now = func() time.Time { return now }
	c.put(cacheEntry{verdict: &VerifiedVerdict{Verdict: VerdictCredible}, embedding: []float32{1, 0, 0}})
	if _, ok := c.get([]float32{1, 0, 0}); !ok {
		t.Fatal("entry missing before expiry")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get([]float32{1, 0, 0}); ok {
		t.Error("expired entry still returned")
	}
}

func TestSemanticCacheEvictsOldestOverBound(t *testing.T) {
	t.Parallel()
	// The size bound evicts oldest-first: with room for two, a third put drops the
	// first, so the earliest claim no longer hits while the two most recent do.
	c := newSemanticCache(time.Minute, 0.9, 2)
	c.put(cacheEntry{verdict: &VerifiedVerdict{Rationale: "first"}, embedding: []float32{1, 0, 0}})
	c.put(cacheEntry{verdict: &VerifiedVerdict{Rationale: "second"}, embedding: []float32{0, 1, 0}})
	c.put(cacheEntry{verdict: &VerifiedVerdict{Rationale: "third"}, embedding: []float32{0, 0, 1}})
	if _, ok := c.get([]float32{1, 0, 0}); ok {
		t.Error("oldest entry not evicted past the size bound")
	}
	if got, ok := c.get([]float32{0, 1, 0}); !ok || got.verdict.Rationale != "second" {
		t.Error("second entry evicted, want retained")
	}
	if got, ok := c.get([]float32{0, 0, 1}); !ok || got.verdict.Rationale != "third" {
		t.Error("third (newest) entry missing")
	}
}

func TestSemanticCacheDropsEmptyEmbedding(t *testing.T) {
	t.Parallel()
	// An entry with no embedding is not stored: it could never be matched and, at a
	// low bar, could false-share against another empty-vector claim.
	c := newSemanticCache(time.Minute, 0, 16)
	c.put(cacheEntry{verdict: &VerifiedVerdict{Verdict: VerdictCredible}, embedding: nil})
	if _, ok := c.get(nil); ok {
		t.Error("empty-embedding entry was stored and matched")
	}
	if len(c.entries) != 0 {
		t.Errorf("cache holds %d entries, want 0", len(c.entries))
	}
}
