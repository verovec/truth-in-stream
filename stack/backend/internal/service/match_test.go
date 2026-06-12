package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// scoreApprox absorbs the float32 distance to float64 score conversion.
var scoreApprox = cmpopts.EquateApprox(0, 1e-6)

type fakeEmbedder struct {
	vecs     [][]float32
	err      error
	gotTexts []string
	calls    int
}

func (f *fakeEmbedder) EmbedQueries(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.gotTexts = texts
	if f.err != nil {
		return nil, f.err
	}
	return f.vecs, nil
}

type fakeSearcher struct {
	hits     []domain.ClaimMatch
	err      error
	gotQuery []float32
	gotTopK  int
}

func (f *fakeSearcher) Search(_ context.Context, query []float32, topK int) ([]domain.ClaimMatch, error) {
	f.gotQuery = query
	f.gotTopK = topK
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

type fakeEvidence struct {
	hits     []domain.WikiEvidence
	err      error
	gotQuery []float32
	gotTopK  int
}

func (f *fakeEvidence) SearchWiki(_ context.Context, query []float32, topK int) ([]domain.WikiEvidence, error) {
	f.gotQuery = query
	f.gotTopK = topK
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func queryVec() []float32 {
	v := make([]float32, domain.EmbeddingDim)
	v[0] = 1
	return v
}

// testMatcherConfig disables evidence retrieval (EvidenceTopK 0) so the
// claims-only cases are unaffected by the merge path; MaxResults is generous so
// nothing is truncated. Tests that exercise evidence override these.
func testMatcherConfig() MatcherConfig {
	return MatcherConfig{TopK: 3, ScoreThreshold: 0.5, EvidenceTopK: 0, EvidenceThreshold: 0.6, MaxResults: 10, EmbedConcurrency: 4, Timeout: time.Minute}
}

func TestMatchSegment(t *testing.T) {
	t.Parallel()

	hit := func(id string, distance float32) domain.ClaimMatch {
		return domain.ClaimMatch{
			ID:       id,
			Text:     "text " + id,
			Verdict:  domain.VerdictContradicts,
			Sources:  []domain.Source{{Title: "t " + id, URL: "https://" + id}},
			Distance: distance,
		}
	}
	match := func(id string, score float64) Match {
		return Match{
			Kind:    domain.MatchKindClaim,
			ClaimID: id,
			Text:    "text " + id,
			Verdict: domain.VerdictContradicts,
			Sources: []domain.Source{{Title: "t " + id, URL: "https://" + id}},
			Score:   score,
		}
	}

	tests := []struct {
		name      string
		segment   string
		embedder  *fakeEmbedder
		searcher  *fakeSearcher
		cfg       MatcherConfig
		want      []Match
		wantErr   bool
		wantErrIs error
	}{
		{
			name:     "ranked matches carry similarity scores",
			segment:  "the wall is visible from space",
			embedder: &fakeEmbedder{vecs: [][]float32{queryVec()}},
			searcher: &fakeSearcher{hits: []domain.ClaimMatch{hit("a", 0.1), hit("b", 0.3)}},
			cfg:      testMatcherConfig(),
			want:     []Match{match("a", 0.9), match("b", 0.7)},
		},
		{
			name:     "matches below threshold excluded",
			segment:  "loosely related chatter",
			embedder: &fakeEmbedder{vecs: [][]float32{queryVec()}},
			searcher: &fakeSearcher{hits: []domain.ClaimMatch{hit("a", 0.2), hit("b", 0.6), hit("c", 0.9)}},
			cfg:      testMatcherConfig(),
			want:     []Match{match("a", 0.8)},
		},
		{
			name:     "score equal to threshold kept",
			segment:  "borderline segment",
			embedder: &fakeEmbedder{vecs: [][]float32{queryVec()}},
			searcher: &fakeSearcher{hits: []domain.ClaimMatch{hit("a", 0.5)}},
			cfg:      testMatcherConfig(),
			want:     []Match{match("a", 0.5)},
		},
		{
			name:     "no hits gives empty result",
			segment:  "anything",
			embedder: &fakeEmbedder{vecs: [][]float32{queryVec()}},
			searcher: &fakeSearcher{},
			cfg:      testMatcherConfig(),
			want:     []Match{},
		},
		{
			name:      "empty segment rejected",
			segment:   "",
			embedder:  &fakeEmbedder{},
			searcher:  &fakeSearcher{},
			cfg:       testMatcherConfig(),
			wantErrIs: ErrEmptySegment,
		},
		{
			name:      "whitespace segment rejected",
			segment:   "  \n\t ",
			embedder:  &fakeEmbedder{},
			searcher:  &fakeSearcher{},
			cfg:       testMatcherConfig(),
			wantErrIs: ErrEmptySegment,
		},
		{
			name:     "embedder error propagates",
			segment:  "anything",
			embedder: &fakeEmbedder{err: errors.New("rate limited")},
			searcher: &fakeSearcher{},
			cfg:      testMatcherConfig(),
			wantErr:  true,
		},
		{
			name:     "searcher error propagates",
			segment:  "anything",
			embedder: &fakeEmbedder{vecs: [][]float32{queryVec()}},
			searcher: &fakeSearcher{err: errors.New("connection refused")},
			cfg:      testMatcherConfig(),
			wantErr:  true,
		},
		{
			name:     "wrong embedding count fails fast",
			segment:  "anything",
			embedder: &fakeEmbedder{vecs: [][]float32{queryVec(), queryVec()}},
			searcher: &fakeSearcher{},
			cfg:      testMatcherConfig(),
			wantErr:  true,
		},
		{
			name:     "dimension mismatch fails fast",
			segment:  "anything",
			embedder: &fakeEmbedder{vecs: [][]float32{make([]float32, 512)}},
			searcher: &fakeSearcher{hits: []domain.ClaimMatch{hit("a", 0.1)}},
			cfg:      testMatcherConfig(),
			wantErr:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := NewMatcher(tc.embedder, tc.searcher, &fakeEvidence{}, tc.cfg)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}

			got, err := m.MatchSegment(t.Context(), tc.segment)
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				return
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("MatchSegment: %v", err)
			}

			if diff := cmp.Diff(tc.want, got, scoreApprox); diff != "" {
				t.Errorf("matches mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{tc.segment}, tc.embedder.gotTexts); diff != "" {
				t.Errorf("embedded texts mismatch (-want +got):\n%s", diff)
			}
			if tc.searcher.gotTopK != tc.cfg.TopK {
				t.Errorf("search topK = %d, want %d", tc.searcher.gotTopK, tc.cfg.TopK)
			}
			if diff := cmp.Diff(queryVec(), tc.searcher.gotQuery); diff != "" {
				t.Errorf("search query mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMatchSegmentMergesClaimsAndEvidence(t *testing.T) {
	t.Parallel()

	claimHit := func(id string, distance float32) domain.ClaimMatch {
		return domain.ClaimMatch{
			ID:       id,
			Text:     "claim " + id,
			Verdict:  domain.VerdictCorroborates,
			Sources:  []domain.Source{{Title: "S " + id, URL: "https://c/" + id}},
			Distance: distance,
		}
	}
	evidenceHit := func(title string, distance float32) domain.WikiEvidence {
		return domain.WikiEvidence{
			Title:    title,
			URL:      "https://en.wikipedia.org/wiki/" + title,
			Content:  "lead " + title,
			Distance: distance,
		}
	}

	cfg := MatcherConfig{
		TopK: 5, ScoreThreshold: 0.5,
		EvidenceTopK: 4, EvidenceThreshold: 0.6,
		MaxResults: 3, EmbedConcurrency: 4, Timeout: time.Minute,
	}
	searcher := &fakeSearcher{hits: []domain.ClaimMatch{claimHit("a", 0.05), claimHit("b", 0.5)}}
	// ev2 at distance 0.45 scores 0.55, below the 0.6 evidence threshold, so it
	// is dropped even though it would clear the laxer claim threshold.
	evidence := &fakeEvidence{hits: []domain.WikiEvidence{evidenceHit("Wall", 0.1), evidenceHit("Trivia", 0.45)}}

	m, err := NewMatcher(&fakeEmbedder{vecs: [][]float32{queryVec()}}, searcher, evidence, cfg)
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	got, err := m.MatchSegment(t.Context(), "the great wall is very old")
	if err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}

	want := []Match{
		{Kind: domain.MatchKindClaim, ClaimID: "a", Text: "claim a", Verdict: domain.VerdictCorroborates, Sources: []domain.Source{{Title: "S a", URL: "https://c/a"}}, Score: 0.95},
		{Kind: domain.MatchKindEvidence, Text: "lead Wall", Article: domain.Article{Title: "Wall", URL: "https://en.wikipedia.org/wiki/Wall"}, Score: 0.9},
		{Kind: domain.MatchKindClaim, ClaimID: "b", Text: "claim b", Verdict: domain.VerdictCorroborates, Sources: []domain.Source{{Title: "S b", URL: "https://c/b"}}, Score: 0.5},
	}
	if diff := cmp.Diff(want, got, scoreApprox); diff != "" {
		t.Errorf("merged matches mismatch (-want +got):\n%s", diff)
	}
	if evidence.gotTopK != cfg.EvidenceTopK {
		t.Errorf("evidence topK = %d, want %d", evidence.gotTopK, cfg.EvidenceTopK)
	}
	if diff := cmp.Diff(queryVec(), evidence.gotQuery); diff != "" {
		t.Errorf("evidence query mismatch (-want +got):\n%s", diff)
	}
}

func TestMatchSegmentDisabledEvidenceSkipsSearch(t *testing.T) {
	t.Parallel()

	cfg := testMatcherConfig() // EvidenceTopK 0
	searcher := &fakeSearcher{hits: []domain.ClaimMatch{{ID: "a", Text: "claim a", Verdict: domain.VerdictUnclear, Distance: 0.1}}}
	evidence := &fakeEvidence{hits: []domain.WikiEvidence{{Title: "X", URL: "https://x", Content: "y", Distance: 0}}}

	m, err := NewMatcher(&fakeEmbedder{vecs: [][]float32{queryVec()}}, searcher, evidence, cfg)
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	got, err := m.MatchSegment(t.Context(), "anything")
	if err != nil {
		t.Fatalf("MatchSegment: %v", err)
	}
	for _, mt := range got {
		if mt.Kind == domain.MatchKindEvidence {
			t.Errorf("evidence surfaced while disabled: %+v", mt)
		}
	}
	if evidence.gotQuery != nil {
		t.Error("evidence searcher was called while disabled")
	}
}

func TestMatchSegmentEvidenceErrorPropagates(t *testing.T) {
	t.Parallel()

	cfg := testMatcherConfig()
	cfg.EvidenceTopK = 3
	evidence := &fakeEvidence{err: errors.New("wiki search down")}

	m, err := NewMatcher(&fakeEmbedder{vecs: [][]float32{queryVec()}}, &fakeSearcher{}, evidence, cfg)
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if _, err := m.MatchSegment(t.Context(), "anything"); err == nil {
		t.Fatal("expected evidence error, got nil")
	}
}

func TestMatchSegmentRejectsDimensionMismatchBeforeSearch(t *testing.T) {
	t.Parallel()
	searcher := &fakeSearcher{}
	evidence := &fakeEvidence{}
	m, err := NewMatcher(&fakeEmbedder{vecs: [][]float32{{1, 2, 3}}}, searcher, evidence, testMatcherConfig())
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	if _, err := m.MatchSegment(t.Context(), "segment"); err == nil {
		t.Fatal("expected dimension error, got nil")
	}
	if searcher.gotQuery != nil {
		t.Error("search was called with a wrong-dimension vector")
	}
	if evidence.gotQuery != nil {
		t.Error("evidence search was called with a wrong-dimension vector")
	}
}

func TestNewMatcherValidatesConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*MatcherConfig)
		wantErr bool
	}{
		{name: "valid config accepted", mutate: func(*MatcherConfig) {}},
		{name: "zero top k rejected", mutate: func(c *MatcherConfig) { c.TopK = 0 }, wantErr: true},
		{name: "top k beyond int32 rejected", mutate: func(c *MatcherConfig) { c.TopK = math.MaxInt32 + 1 }, wantErr: true},
		{name: "NaN threshold rejected", mutate: func(c *MatcherConfig) { c.ScoreThreshold = math.NaN() }, wantErr: true},
		{name: "threshold above 1 rejected", mutate: func(c *MatcherConfig) { c.ScoreThreshold = 1.5 }, wantErr: true},
		{name: "threshold below -1 rejected", mutate: func(c *MatcherConfig) { c.ScoreThreshold = -1.5 }, wantErr: true},
		{name: "zero evidence top k accepted", mutate: func(c *MatcherConfig) { c.EvidenceTopK = 0 }},
		{name: "negative evidence top k rejected", mutate: func(c *MatcherConfig) { c.EvidenceTopK = -1 }, wantErr: true},
		{name: "NaN evidence threshold rejected", mutate: func(c *MatcherConfig) { c.EvidenceThreshold = math.NaN() }, wantErr: true},
		{name: "evidence threshold above 1 rejected", mutate: func(c *MatcherConfig) { c.EvidenceThreshold = 1.5 }, wantErr: true},
		{name: "zero max results rejected", mutate: func(c *MatcherConfig) { c.MaxResults = 0 }, wantErr: true},
		{name: "zero concurrency rejected", mutate: func(c *MatcherConfig) { c.EmbedConcurrency = 0 }, wantErr: true},
		{name: "zero timeout rejected", mutate: func(c *MatcherConfig) { c.Timeout = 0 }, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testMatcherConfig()
			tc.mutate(&cfg)
			_, err := NewMatcher(&fakeEmbedder{}, &fakeSearcher{}, &fakeEvidence{}, cfg)
			if tc.wantErr != (err != nil) {
				t.Fatalf("NewMatcher err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// stubSearcher returns no hits and records nothing, so concurrent calls are
// race-free.
type stubSearcher struct{}

func (stubSearcher) Search(context.Context, []float32, int) ([]domain.ClaimMatch, error) {
	return nil, nil
}

// stubEvidence is the evidence-corpus counterpart to stubSearcher.
type stubEvidence struct{}

func (stubEvidence) SearchWiki(context.Context, []float32, int) ([]domain.WikiEvidence, error) {
	return nil, nil
}

// gatedEmbedder reports each call on started, then blocks until release is
// closed or the context ends.
type gatedEmbedder struct {
	started chan struct{}
	release chan struct{}
}

func (g *gatedEmbedder) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error) {
	g.started <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, domain.EmbeddingDim)
	}
	return out, nil
}

func TestMatcherBoundsEmbedConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const segments = 5
		const limit = 2

		embedder := &gatedEmbedder{
			started: make(chan struct{}, segments),
			release: make(chan struct{}),
		}
		cfg := testMatcherConfig()
		cfg.EmbedConcurrency = limit
		m, err := NewMatcher(embedder, stubSearcher{}, stubEvidence{}, cfg)
		if err != nil {
			t.Fatalf("NewMatcher: %v", err)
		}

		var wg sync.WaitGroup
		errs := make([]error, segments)
		for i := range segments {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = m.MatchSegment(t.Context(), fmt.Sprintf("segment %d", i))
			}()
		}

		for range limit {
			<-embedder.started
		}
		synctest.Wait()
		if extra := len(embedder.started); extra != 0 {
			t.Fatalf("%d embed calls in flight beyond the limit of %d", extra, limit)
		}

		close(embedder.release)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("segment %d: %v", i, err)
			}
		}
	})
}

func TestMatcherTimeoutCancelsSlowEmbed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		embedder := &gatedEmbedder{
			started: make(chan struct{}, 1),
			release: make(chan struct{}),
		}
		cfg := testMatcherConfig()
		cfg.Timeout = 10 * time.Millisecond
		m, err := NewMatcher(embedder, stubSearcher{}, stubEvidence{}, cfg)
		if err != nil {
			t.Fatalf("NewMatcher: %v", err)
		}

		_, err = m.MatchSegment(t.Context(), "slow segment")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	})
}

func TestMatcherSlotWaitRespectsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		embedder := &gatedEmbedder{
			started: make(chan struct{}, 2),
			release: make(chan struct{}),
		}
		cfg := testMatcherConfig()
		cfg.EmbedConcurrency = 1
		m, err := NewMatcher(embedder, stubSearcher{}, stubEvidence{}, cfg)
		if err != nil {
			t.Fatalf("NewMatcher: %v", err)
		}

		go func() {
			_, _ = m.MatchSegment(t.Context(), "slot holder")
		}()
		<-embedder.started

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = m.MatchSegment(ctx, "waiter")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if !strings.Contains(err.Error(), "embed slot") {
			t.Errorf("err = %q, want mention of the embed slot wait", err)
		}

		close(embedder.release)
	})
}
