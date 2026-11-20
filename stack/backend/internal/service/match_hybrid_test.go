package service

import (
	"context"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// hybridClaimFake implements both ClaimSearcher and HybridClaimSearcher and
// records which path the matcher took and with what arguments.
type hybridClaimFake struct {
	hits          []domain.ClaimMatch
	hybridCalled  bool
	vectorCalled  bool
	gotText       string
	gotTopK       int
	gotLexicalK   int
	gotRRFK       int
	gotHybridVec  []float32
	gotVectorTopK int
}

func (f *hybridClaimFake) Search(_ context.Context, _ []float32, topK, _ int) ([]domain.ClaimMatch, error) {
	f.vectorCalled = true
	f.gotVectorTopK = topK
	return f.hits, nil
}

func (f *hybridClaimFake) SearchHybrid(_ context.Context, text string, query []float32, topK, lexicalK, rrfK, _ int) ([]domain.ClaimMatch, error) {
	f.hybridCalled = true
	f.gotText = text
	f.gotHybridVec = query
	f.gotTopK = topK
	f.gotLexicalK = lexicalK
	f.gotRRFK = rrfK
	return f.hits, nil
}

func hybridMatcherConfig() MatcherConfig {
	cfg := testMatcherConfig()
	cfg.HybridSearch = true
	cfg.LexicalTopK = 20
	cfg.RRFK = 60
	return cfg
}

func TestMatchSegmentUsesHybridWhenSupported(t *testing.T) {
	t.Parallel()
	claims := &hybridClaimFake{hits: []domain.ClaimMatch{{ID: "c1", Text: "x", Verdict: domain.VerdictCorroborates, Distance: 0.1}}}
	m, err := NewMatcher(&fakeEmbedder{vecs: [][]float32{queryVec()}}, claims, &fakeEvidence{}, hybridMatcherConfig())
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if _, _, err := m.MatchSegment(context.Background(), "le chomage a 9,7 pour cent"); err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}
	if !claims.hybridCalled || claims.vectorCalled {
		t.Fatalf("expected the hybrid path, got hybrid=%v vector=%v", claims.hybridCalled, claims.vectorCalled)
	}
	if claims.gotText != "le chomage a 9,7 pour cent" {
		t.Fatalf("hybrid did not receive the segment text: %q", claims.gotText)
	}
	if claims.gotTopK != 3 || claims.gotLexicalK != 20 || claims.gotRRFK != 60 {
		t.Fatalf("hybrid knobs mismatched: topK=%d lexicalK=%d rrfK=%d", claims.gotTopK, claims.gotLexicalK, claims.gotRRFK)
	}
}

func TestMatchSegmentUsesVectorWhenHybridDisabled(t *testing.T) {
	t.Parallel()
	claims := &hybridClaimFake{hits: nil}
	cfg := hybridMatcherConfig()
	cfg.HybridSearch = false
	m, err := NewMatcher(&fakeEmbedder{vecs: [][]float32{queryVec()}}, claims, &fakeEvidence{}, cfg)
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if _, _, err := m.MatchSegment(context.Background(), "un fait"); err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}
	if claims.hybridCalled || !claims.vectorCalled {
		t.Fatalf("expected the vector path when hybrid disabled, got hybrid=%v vector=%v", claims.hybridCalled, claims.vectorCalled)
	}
}

// TestMatchSegmentFallsBackWhenStoreLacksHybrid proves hybrid is additive: a
// store that only implements ClaimSearcher (fakeSearcher) is transparently used
// vector-only even with hybrid configured on, so no implementer is forced to add
// the capability.
func TestMatchSegmentFallsBackWhenStoreLacksHybrid(t *testing.T) {
	t.Parallel()
	claims := &fakeSearcher{hits: nil}
	m, err := NewMatcher(&fakeEmbedder{vecs: [][]float32{queryVec()}}, claims, &fakeEvidence{}, hybridMatcherConfig())
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if _, _, err := m.MatchSegment(context.Background(), "un fait"); err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}
	if claims.gotTopK != 3 {
		t.Fatalf("expected the vector Search fallback (topK 3), got %d", claims.gotTopK)
	}
}

func TestMatcherConfigRejectsBadHybridKnobs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mut  func(*MatcherConfig)
	}{
		{"zero lexical top k", func(c *MatcherConfig) { c.LexicalTopK = 0 }},
		{"zero rrf constant", func(c *MatcherConfig) { c.RRFK = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hybridMatcherConfig()
			tc.mut(&cfg)
			if _, err := NewMatcher(&fakeEmbedder{}, &fakeSearcher{}, &fakeEvidence{}, cfg); err == nil {
				t.Fatal("expected NewMatcher to reject the config")
			}
		})
	}
}
