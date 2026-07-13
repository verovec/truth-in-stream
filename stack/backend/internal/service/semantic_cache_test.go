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
			got, ok := c.get(tc.query, false)
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
			if _, ok := c.get(tc.query, false); ok {
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
	got, ok := c.get([]float32{1, 0, 0}, false)
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
	if _, ok := c.get([]float32{1, 0, 0}, false); !ok {
		t.Fatal("entry missing before expiry")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get([]float32{1, 0, 0}, false); ok {
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
	if _, ok := c.get([]float32{1, 0, 0}, false); ok {
		t.Error("oldest entry not evicted past the size bound")
	}
	if got, ok := c.get([]float32{0, 1, 0}, false); !ok || got.verdict.Rationale != "second" {
		t.Error("second entry evicted, want retained")
	}
	if got, ok := c.get([]float32{0, 0, 1}, false); !ok || got.verdict.Rationale != "third" {
		t.Error("third (newest) entry missing")
	}
}

func TestHasNegationDetectsMarkers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"french affirmative", "le chômage a augmenté cette année", false},
		{"french ne-pas", "le chômage n'a pas augmenté cette année", true},
		{"french standalone pas", "pas de hausse du chômage", true},
		{"french jamais", "il n'a jamais baissé les impôts", true},
		{"french aucune", "aucune hausse constatée", true},
		{"french sans", "sans aucune hausse", true},
		{"french non", "non, c'est faux", true},
		{"french plus alone is not a negation", "plus de croissance cette année", false},
		{"english affirmative", "unemployment rose last quarter", false},
		{"english not", "unemployment did not rise last quarter", true},
		{"english contraction n't", "unemployment didn't rise last quarter", true},
		{"english never", "he never lowered taxes", true},
		{"english no", "there was no increase", true},
		{"english without", "growth without inflation", true},
		{"curly apostrophe elision", "le chômage n’a pas augmenté", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasNegation(tc.text); got != tc.want {
				t.Errorf("hasNegation(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestSemanticCacheVetoesNegationPolarityMismatch(t *testing.T) {
	t.Parallel()
	// Two claims embed near-identically (a negation and its affirmation - the case
	// dense embeddings blur), but disagree on negation polarity: the cache must NOT
	// replay the affirmation's verdict for the negation, even above the bar. A
	// same-polarity paraphrase still hits, so the veto does not kill all reuse.
	vec := []float32{1, 0, 0}
	c := newSemanticCache(time.Minute, 0.95, 16)
	c.put(cacheEntry{source: SourceVerified, verdict: &VerifiedVerdict{Verdict: VerdictCredible}, embedding: vec, negated: false})

	if _, ok := c.get(vec, true); ok {
		t.Error("negation-polarity mismatch hit the cache; a negated claim must not replay an affirmation's verdict")
	}
	if _, ok := c.get(vec, false); !ok {
		t.Error("same-polarity paraphrase missed; the veto must not kill legitimate hits")
	}
}

func TestVerifyPathCacheVetoesNegatedParaphraseText(t *testing.T) {
	t.Parallel()
	// End-to-end through the VerifyPath cache wrappers, which derive negation from
	// the claim text: an affirmation's verdict is cached, then a negated paraphrase
	// with the SAME retrieval embedding is denied the hit while a same-polarity
	// paraphrase is served.
	vp, err := NewVerifyPath(VerifyPathConfig{
		Decomposer: fakeDecomposer{}, Matcher: liveMatcher{}, Verifier: &fakeVerifier{},
		FastTau: 0.85, VerifyConcurrency: 1, FastDeadline: time.Second, VerifyDeadline: time.Second,
		CacheTTL: time.Minute, CacheThreshold: 0.9, CacheMaxEntries: 16,
	})
	if err != nil {
		t.Fatalf("NewVerifyPath: %v", err)
	}
	vec := []float32{0.6, 0.8}
	vp.cachePut(vec, "le chômage a augmenté", SourceVerified, &VerifiedVerdict{Verdict: VerdictCredible})

	if _, ok := vp.cacheGet(vec, "le chômage n'a pas augmenté"); ok {
		t.Error("negated paraphrase hit the cache; must be vetoed and re-verified fresh")
	}
	if _, ok := vp.cacheGet(vec, "le chômage a beaucoup augmenté"); !ok {
		t.Error("same-polarity paraphrase missed; a genuine repeat should still hit")
	}
}

func TestSemanticCachePutReplacesSameEmbedding(t *testing.T) {
	t.Parallel()
	// A claim re-cached under the same embedding (verify -> terminal-gate upgrade)
	// replaces its entry in place rather than appending, so it holds one slot with
	// the upgraded verdict, not two.
	vec := []float32{1, 0, 0}
	c := newSemanticCache(time.Minute, 0.9, 16)
	c.put(cacheEntry{verdict: &VerifiedVerdict{Rationale: "fast"}, embedding: vec})
	c.put(cacheEntry{verdict: &VerifiedVerdict{Rationale: "upgraded"}, embedding: vec})
	if len(c.entries) != 1 {
		t.Fatalf("cache holds %d entries, want 1 (same embedding replaced in place)", len(c.entries))
	}
	got, ok := c.get(vec, false)
	if !ok || got.verdict.Rationale != "upgraded" {
		t.Errorf("hit = %+v, want the upgraded verdict", got.verdict)
	}
}

func TestSemanticCacheDropsEmptyEmbedding(t *testing.T) {
	t.Parallel()
	// An entry with no embedding is not stored: it could never be matched and, at a
	// low bar, could false-share against another empty-vector claim.
	c := newSemanticCache(time.Minute, 0, 16)
	c.put(cacheEntry{verdict: &VerifiedVerdict{Verdict: VerdictCredible}, embedding: nil})
	if _, ok := c.get(nil, false); ok {
		t.Error("empty-embedding entry was stored and matched")
	}
	if len(c.entries) != 0 {
		t.Errorf("cache holds %d entries, want 0", len(c.entries))
	}
}
