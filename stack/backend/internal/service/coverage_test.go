package service

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// coverage test corpora. Score is 1 - distance, so a smaller distance is a
// stronger hit. The floors and distances are exact float32 quarters, so the
// inclusive-boundary cases compare cleanly rather than tripping over the
// float32-to-float64 rounding that shifts, say, distance 0.6 just off 0.4.
const (
	testClaimsThreshold = 0.5  // covered iff distance <= 0.5
	testWikiThreshold   = 0.75 // covered iff distance <= 0.25
)

func claimHits(distance float32) []domain.ClaimMatch {
	return []domain.ClaimMatch{{ID: "c", Distance: distance}}
}

func wikiHits(distance float32) []domain.EvidenceHit {
	return []domain.EvidenceHit{{Title: "w", Distance: distance}}
}

func newCombined(t *testing.T, embedder QueryEmbedder, claims ClaimSearcher, wiki EvidenceSearcher, cfg CoverageConfig) *CombinedCoverage {
	t.Helper()
	c, err := NewCombinedCoverage(embedder, claims, wiki, cfg)
	if err != nil {
		t.Fatalf("NewCombinedCoverage: %v", err)
	}
	return c
}

func bothEnabled() CoverageConfig {
	return CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiThreshold: testWikiThreshold, WikiEnabled: true}
}

func TestCombinedCoverageEitherCorpusCovers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		claimDist   float32 // score 1-dist vs claims floor 0.5
		wikiDist    float32 // score 1-dist vs wiki floor 0.75
		wantCovered bool
	}{
		{"claims covers, wiki does not", 0.25, 0.5, true},  // claims 0.75, wiki 0.5
		{"wiki covers, claims does not", 0.75, 0.25, true}, // claims 0.25, wiki 0.75
		{"both cover", 0.25, 0.25, true},                   // claims 0.75, wiki 0.75
		{"neither covers", 0.75, 0.5, false},               // claims 0.25, wiki 0.5
		{"claims at floor (inclusive)", 0.5, 0.5, true},    // claims 0.5 == floor
		{"wiki at floor (inclusive)", 0.75, 0.25, true},    // wiki 0.75 == floor
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
			c := newCombined(t, embedder,
				&fakeSearcher{hits: claimHits(tc.claimDist)},
				&fakeEvidence{hits: wikiHits(tc.wikiDist)},
				bothEnabled())
			got, err := c.Covered(t.Context(), "a factual statement")
			if err != nil {
				t.Fatalf("Covered: %v", err)
			}
			if got != tc.wantCovered {
				t.Errorf("Covered = %v, want %v", got, tc.wantCovered)
			}
		})
	}
}

func TestCombinedCoverageUsesRecallEfSearch(t *testing.T) {
	t.Parallel()
	// Coverage is recall-critical, so its evidence probe raises ef_search above
	// the session default (defaultCoverageEfSearch), unfiltered (nil sources). This
	// pins the per-call-site threading the unified search builder adds - a
	// different call site (the political top-1) passes 0 to keep the default.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	wiki := &fakeEvidence{hits: wikiHits(0.25)}
	c := newCombined(t, embedder, &fakeSearcher{hits: claimHits(0.75)}, wiki, bothEnabled())
	if _, err := c.Covered(t.Context(), "a factual statement"); err != nil {
		t.Fatalf("Covered: %v", err)
	}
	if wiki.gotEfSearch != defaultCoverageEfSearch {
		t.Errorf("coverage evidence probe ef_search = %d, want %d", wiki.gotEfSearch, defaultCoverageEfSearch)
	}
	if wiki.gotSources != nil {
		t.Errorf("coverage evidence probe sources = %v, want nil (global)", wiki.gotSources)
	}
}

func TestCombinedCoverageThreadsConfiguredEfSearch(t *testing.T) {
	t.Parallel()
	// A configured CoverageConfig.EfSearch is threaded into both coverage corpora,
	// so an operator can retune the probe budget from PRECHECK_COVERAGE_EF_SEARCH
	// without touching the matcher's per-corpus ef_search.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	claims := &fakeSearcher{hits: claimHits(0.75)}
	wiki := &fakeEvidence{hits: wikiHits(0.75)} // below its floor, so both corpora are probed
	cfg := bothEnabled()
	cfg.EfSearch = 321
	c := newCombined(t, embedder, claims, wiki, cfg)
	if _, err := c.Covered(t.Context(), "a factual statement"); err != nil {
		t.Fatalf("Covered: %v", err)
	}
	if claims.gotEfSearch != 321 {
		t.Errorf("claims coverage ef_search = %d, want 321", claims.gotEfSearch)
	}
	if wiki.gotEfSearch != 321 {
		t.Errorf("wiki coverage ef_search = %d, want 321", wiki.gotEfSearch)
	}
}

func TestCombinedCoverageEmbedsOnceAndSharesVector(t *testing.T) {
	t.Parallel()
	// Claims miss (score 0.25 < 0.5) so both corpora are probed; wiki covers.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	claims := &fakeSearcher{hits: claimHits(0.75)}
	wiki := &fakeEvidence{hits: wikiHits(0.25)}
	c := newCombined(t, embedder, claims, wiki, bothEnabled())

	covered, err := c.Covered(t.Context(), "the statement")
	if err != nil {
		t.Fatalf("Covered: %v", err)
	}
	if !covered {
		t.Fatal("Covered = false, want true")
	}
	if embedder.calls != 1 {
		t.Errorf("embedder called %d times, want exactly 1 (vector must be reused across corpora)", embedder.calls)
	}
	if diff := cmp.Diff(queryVec(), claims.gotQuery); diff != "" {
		t.Errorf("claims search vector mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(queryVec(), wiki.gotQuery); diff != "" {
		t.Errorf("wiki search vector mismatch (-want +got):\n%s", diff)
	}
}

func TestCombinedCoverageWikiDisabledIsClaimsOnly(t *testing.T) {
	t.Parallel()
	// The wiki corpus would cover (distance 0 -> score 1), but with wiki
	// coverage disabled it must never be consulted; claims alone decide.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	wiki := &fakeEvidence{hits: wikiHits(0)}
	c := newCombined(t, embedder,
		&fakeSearcher{hits: claimHits(0.75)}, // claims score 0.25 < 0.5
		wiki,
		CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiThreshold: testWikiThreshold, WikiEnabled: false})

	covered, err := c.Covered(t.Context(), "a statement the wiki covers")
	if err != nil {
		t.Fatalf("Covered: %v", err)
	}
	if covered {
		t.Error("Covered = true, want false: wiki coverage was disabled yet still decided")
	}
	if wiki.gotQuery != nil {
		t.Errorf("wiki searcher was consulted (%v) while disabled", wiki.gotQuery)
	}
}

func TestCombinedCoverageEmptyWikiDegradesToClaims(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		claimDist   float32
		wantCovered bool
	}{
		{"claims cover, empty wiki", 0.25, true}, // claims 0.75 >= 0.5
		{"claims miss, empty wiki", 0.75, false}, // claims 0.25 < 0.5, nothing else
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newCombined(t, &fakeEmbedder{vecs: [][]float32{queryVec()}},
				&fakeSearcher{hits: claimHits(tc.claimDist)},
				&fakeEvidence{hits: nil}, // empty corpus -> found=false
				bothEnabled())
			got, err := c.Covered(t.Context(), "a statement")
			if err != nil {
				t.Fatalf("Covered: %v", err)
			}
			if got != tc.wantCovered {
				t.Errorf("Covered = %v, want %v", got, tc.wantCovered)
			}
		})
	}
}

func TestCombinedCoverageBlankText(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	c := newCombined(t, embedder, &fakeSearcher{}, &fakeEvidence{}, bothEnabled())
	covered, err := c.Covered(t.Context(), "   ")
	if err != nil {
		t.Fatalf("Covered: %v", err)
	}
	if covered {
		t.Error("Covered = true for blank text, want false")
	}
	if embedder.calls != 0 {
		t.Errorf("blank text was embedded (%d calls); it must short-circuit", embedder.calls)
	}
}

func TestCombinedCoverageEmbedError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("voyage down")
	c := newCombined(t, &fakeEmbedder{err: sentinel}, &fakeSearcher{}, &fakeEvidence{}, bothEnabled())
	if _, err := c.Covered(t.Context(), "a claim"); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping %v", err, sentinel)
	}
}

func TestCombinedCoverageDimMismatch(t *testing.T) {
	t.Parallel()
	c := newCombined(t, &fakeEmbedder{vecs: [][]float32{{1, 2, 3}}}, &fakeSearcher{}, &fakeEvidence{}, bothEnabled())
	if _, err := c.Covered(t.Context(), "a claim"); err == nil {
		t.Fatal("err = nil for wrong embedding dimension, want error")
	}
}

func TestCombinedCoverageSearchErrorPropagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("store down")
	t.Run("claims search error", func(t *testing.T) {
		t.Parallel()
		c := newCombined(t, &fakeEmbedder{vecs: [][]float32{queryVec()}},
			&fakeSearcher{err: sentinel}, &fakeEvidence{}, bothEnabled())
		if _, err := c.Covered(t.Context(), "a claim"); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want wrapping %v", err, sentinel)
		}
	})
	t.Run("wiki search error", func(t *testing.T) {
		t.Parallel()
		// Claims miss so the scan reaches the failing wiki corpus.
		c := newCombined(t, &fakeEmbedder{vecs: [][]float32{queryVec()}},
			&fakeSearcher{hits: claimHits(0.75)}, &fakeEvidence{err: sentinel}, bothEnabled())
		if _, err := c.Covered(t.Context(), "a claim"); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want wrapping %v", err, sentinel)
		}
	})
}

func TestNewCombinedCoverageRejectsBadThreshold(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	for _, bad := range []float64{-1.5, 1.5} {
		if _, err := NewCombinedCoverage(embedder, &fakeSearcher{}, &fakeEvidence{},
			CoverageConfig{ClaimsThreshold: bad, WikiThreshold: testWikiThreshold, WikiEnabled: true}); err == nil {
			t.Errorf("claims threshold %v: err = nil, want error", bad)
		}
		if _, err := NewCombinedCoverage(embedder, &fakeSearcher{}, &fakeEvidence{},
			CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiThreshold: bad, WikiEnabled: true}); err == nil {
			t.Errorf("wiki threshold %v (enabled): err = nil, want error", bad)
		}
	}
	// A malformed wiki threshold is rejected even when wiki coverage is
	// disabled, so the bad value fails fast at startup instead of lurking until
	// the corpus is enabled - mirroring the config loader, which validates every
	// supplied threshold regardless of the toggles.
	if _, err := NewCombinedCoverage(embedder, &fakeSearcher{}, &fakeEvidence{},
		CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiThreshold: 1.5, WikiEnabled: false}); err == nil {
		t.Error("bad wiki threshold accepted while disabled, want error (validate regardless of toggle)")
	}
}

func TestCombinedCoverageShortCircuitsWhenClaimsCover(t *testing.T) {
	t.Parallel()
	// Claims cover, so the scan returns on the first covering source and the
	// wiki corpus is never consulted. This guards the early return: a broken
	// loop that fell through to wiki would be caught here, not silently pass.
	wiki := &fakeEvidence{hits: wikiHits(0)}
	c := newCombined(t, &fakeEmbedder{vecs: [][]float32{queryVec()}},
		&fakeSearcher{hits: claimHits(0.25)}, // claims score 0.75 >= 0.5
		wiki, bothEnabled())

	covered, err := c.Covered(t.Context(), "a well-covered claim")
	if err != nil {
		t.Fatalf("Covered: %v", err)
	}
	if !covered {
		t.Fatal("Covered = false, want true")
	}
	if wiki.gotQuery != nil {
		t.Errorf("wiki searcher consulted (%v) after claims already covered; the scan must short-circuit", wiki.gotQuery)
	}
}
