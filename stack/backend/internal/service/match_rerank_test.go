package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeReranker records the documents it was asked to rank and returns a fixed
// order, an error, or the context's expiry, whichever is configured.
type fakeReranker struct {
	order    []int
	err      error
	block    bool
	gotQuery string
	gotDocs  []string
	calls    int
}

func (f *fakeReranker) Rank(ctx context.Context, query string, documents []string) ([]int, error) {
	f.calls++
	f.gotQuery = query
	f.gotDocs = documents
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.order, nil
}

func rerankMatcherConfig() MatcherConfig {
	return MatcherConfig{
		TopK:                  2,
		ScoreThreshold:        0.3,
		EvidenceTopK:          2,
		EvidenceThreshold:     0.3,
		MaxResults:            3,
		EmbedConcurrency:      1,
		Timeout:               time.Second,
		ConfidenceClusterSize: 5,
		ConfidenceLeadWeight:  1,
		ConfidenceBodyWeight:  0.6,
		RerankCandidates:      6,
		RerankTimeout:         100 * time.Millisecond,
	}
}

// rerankFixtures returns three claim hits and three evidence hits with strictly
// decreasing cosine similarity: claims at 0.9, 0.8, 0.7 and evidence at 0.65,
// 0.6, 0.5, all above the thresholds.
func rerankFixtures() (*fakeSearcher, *fakeEvidence) {
	claims := &fakeSearcher{hits: []domain.ClaimMatch{
		{ID: "c0", Text: "claim zero", Distance: 0.1},
		{ID: "c1", Text: "claim one", Distance: 0.2},
		{ID: "c2", Text: "claim two", Distance: 0.3},
	}}
	evidence := &fakeEvidence{hits: []domain.EvidenceHit{
		{Source: "wiki", ExternalID: "e0", Content: "evidence zero", Distance: 0.35},
		{Source: "wiki", ExternalID: "e1", Content: "evidence one", Distance: 0.4},
		{Source: "wiki", ExternalID: "e2", Content: "evidence two", Distance: 0.5},
	}}
	return claims, evidence
}

func rerankTestMatcher(t *testing.T, r Reranker, cfg MatcherConfig) (*Matcher, *fakeSearcher, *fakeEvidence) {
	t.Helper()
	claims, evidence := rerankFixtures()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	var opts []MatcherOption
	if r != nil {
		opts = append(opts, WithReranker(r, nil))
	}
	m, err := NewMatcher(embedder, claims, evidence, cfg, opts...)
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	return m, claims, evidence
}

func matchIDs(matches []Match) []string {
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i] = m.EvidenceID
	}
	return ids
}

func TestRerankSelectsSurvivorsKeepsCosineOrder(t *testing.T) {
	t.Parallel()
	// Rerank prefers, in order: evidence two (idx 5, cosine 0.5), claim one
	// (idx 1, 0.8), evidence zero (idx 3, 0.65). The cut must keep exactly that
	// set while the returned order stays cosine-descending.
	r := &fakeReranker{order: []int{5, 1, 3, 0, 2, 4}}
	m, claims, evidence := rerankTestMatcher(t, r, rerankMatcherConfig())

	matches, _, err := m.MatchSegment(context.Background(), "le chomage a baisse")
	if err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}
	want := []string{
		domain.ComposeEvidenceID(domain.MatchKindClaim, "c1", 0),
		domain.ComposeEvidenceID(domain.MatchKindEvidence, "wiki/e0", 0),
		domain.ComposeEvidenceID(domain.MatchKindEvidence, "wiki/e2", 0),
	}
	if got := matchIDs(matches); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("selected = %v, want %v (rerank picks survivors, cosine orders them)", got, want)
	}
	for i := 1; i < len(matches); i++ {
		if matches[i].Score > matches[i-1].Score {
			t.Errorf("matches not cosine-descending at %d: %v > %v", i, matches[i].Score, matches[i-1].Score)
		}
	}
	if claims.gotTopK != 6 || evidence.gotTopK != 6 {
		t.Errorf("store topK = (%d, %d), want widened (6, 6)", claims.gotTopK, evidence.gotTopK)
	}
	if len(r.gotDocs) != 6 {
		t.Errorf("reranker saw %d documents, want the full fused pool of 6", len(r.gotDocs))
	}
	if r.gotQuery != "le chomage a baisse" {
		t.Errorf("reranker query = %q, want the segment text", r.gotQuery)
	}
}

func TestRerankFailuresKeepFusedOrder(t *testing.T) {
	t.Parallel()
	baselineMatcher, _, _ := rerankTestMatcher(t, nil, MatcherConfig{
		TopK: 2, ScoreThreshold: 0.3, EvidenceTopK: 2, EvidenceThreshold: 0.3,
		MaxResults: 3, EmbedConcurrency: 1, Timeout: time.Second,
		ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0.6,
	})
	baseline, _, err := baselineMatcher.MatchSegment(context.Background(), "segment")
	if err != nil {
		t.Fatalf("baseline MatchSegment: %v", err)
	}

	tests := []struct {
		name string
		r    *fakeReranker
	}{
		{"api error", &fakeReranker{err: errors.New("boom")}},
		{"timeout", &fakeReranker{block: true}},
		{"truncated order", &fakeReranker{order: []int{0, 1}}},
		{"duplicate index", &fakeReranker{order: []int{0, 0, 1, 2, 3, 4}}},
		{"out of range", &fakeReranker{order: []int{0, 1, 2, 3, 4, 9}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _, _ := rerankTestMatcher(t, tc.r, rerankMatcherConfig())
			matches, _, err := m.MatchSegment(context.Background(), "segment")
			if err != nil {
				t.Fatalf("MatchSegment: %v", err)
			}
			if fmt.Sprint(matchIDs(matches)) != fmt.Sprint(matchIDs(baseline)) {
				t.Errorf("fail-open selection = %v, want fused baseline %v", matchIDs(matches), matchIDs(baseline))
			}
			if tc.r.calls != 1 {
				t.Errorf("reranker calls = %d, want 1", tc.r.calls)
			}
		})
	}
}

func TestRerankSkippedWhenPoolFitsCut(t *testing.T) {
	t.Parallel()
	cfg := rerankMatcherConfig()
	cfg.MaxResults = 6
	r := &fakeReranker{order: []int{0, 1, 2, 3, 4, 5}}
	m, _, _ := rerankTestMatcher(t, r, cfg)

	if _, _, err := m.MatchSegment(context.Background(), "segment"); err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}
	if r.calls != 0 {
		t.Errorf("reranker calls = %d, want 0 when the pool fits the cut", r.calls)
	}
}

func TestNoRerankerKeepsConfiguredTopK(t *testing.T) {
	t.Parallel()
	cfg := rerankMatcherConfig()
	m, claims, evidence := rerankTestMatcher(t, nil, cfg)

	if _, _, err := m.MatchSegment(context.Background(), "segment"); err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}
	if claims.gotTopK != cfg.TopK || evidence.gotTopK != cfg.EvidenceTopK {
		t.Errorf("store topK = (%d, %d), want unwidened (%d, %d) without a reranker",
			claims.gotTopK, evidence.gotTopK, cfg.TopK, cfg.EvidenceTopK)
	}
}

func TestNewMatcherValidatesRerankConfig(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	claims, evidence := rerankFixtures()

	cfg := rerankMatcherConfig()
	cfg.RerankCandidates = 0
	if _, err := NewMatcher(embedder, claims, evidence, cfg, WithReranker(&fakeReranker{}, nil)); err == nil {
		t.Error("NewMatcher with reranker and zero candidates returned nil error")
	}
	cfg = rerankMatcherConfig()
	cfg.RerankTimeout = 0
	if _, err := NewMatcher(embedder, claims, evidence, cfg, WithReranker(&fakeReranker{}, nil)); err == nil {
		t.Error("NewMatcher with reranker and zero timeout returned nil error")
	}
	// Without a reranker the same zero values are fine: the fields are ignored.
	cfg = rerankMatcherConfig()
	cfg.RerankCandidates, cfg.RerankTimeout = 0, 0
	if _, err := NewMatcher(embedder, claims, evidence, cfg); err != nil {
		t.Errorf("NewMatcher without reranker rejected ignored rerank fields: %v", err)
	}
}
