package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type stubSegmentMatcher struct {
	matches []Match
	err     error
	gotText string
}

func (s *stubSegmentMatcher) MatchSegment(_ context.Context, segment string) ([]Match, error) {
	s.gotText = segment
	return s.matches, s.err
}

func TestSegmentMatchAdapterConvertsMatches(t *testing.T) {
	t.Parallel()

	stub := &stubSegmentMatcher{
		matches: []Match{
			{
				Kind:    domain.MatchKindClaim,
				ClaimID: "great-wall-from-space",
				Text:    "The Great Wall of China is visible from space with the naked eye.",
				Verdict: domain.VerdictContradicts,
				Sources: []domain.Source{{Title: "NASA", URL: "https://nasa.gov"}},
				Score:   0.91,
			},
			{
				Kind:    domain.MatchKindEvidence,
				Text:    "The Great Wall of China is a series of fortifications.",
				Article: domain.Article{Title: "Great Wall of China", URL: "https://en.wikipedia.org/wiki/Great_Wall_of_China"},
				Score:   0.8,
			},
			{
				Kind:    domain.MatchKindClaim,
				ClaimID: "everest-highest",
				Text:    "Mount Everest is the highest mountain above sea level on Earth.",
				Verdict: domain.VerdictCorroborates,
				Sources: nil,
				Score:   0.74,
			},
		},
	}
	adapter := NewSegmentMatchAdapter(stub)

	got, err := adapter.Match(context.Background(), "you can see the great wall from orbit")
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}

	want := []domain.SegmentMatch{
		{
			Kind:       domain.MatchKindClaim,
			Claim:      "The Great Wall of China is visible from space with the naked eye.",
			Verdict:    domain.VerdictContradicts,
			Sources:    []domain.Source{{Title: "NASA", URL: "https://nasa.gov"}},
			Similarity: 0.91,
		},
		{
			Kind:       domain.MatchKindEvidence,
			Claim:      "The Great Wall of China is a series of fortifications.",
			Sources:    []domain.Source{},
			Similarity: 0.8,
			Article:    &domain.Article{Title: "Great Wall of China", URL: "https://en.wikipedia.org/wiki/Great_Wall_of_China"},
		},
		{
			// Evidence has no verdict; a claim with no sources normalizes to an
			// empty slice so the wire never carries a null sources array.
			Kind:       domain.MatchKindClaim,
			Claim:      "Mount Everest is the highest mountain above sea level on Earth.",
			Verdict:    domain.VerdictCorroborates,
			Sources:    []domain.Source{},
			Similarity: 0.74,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("converted matches mismatch (-want +got):\n%s", diff)
	}
	if stub.gotText != "you can see the great wall from orbit" {
		t.Errorf("segment text not forwarded, got %q", stub.gotText)
	}
}

func TestSegmentMatchAdapterEvidenceArticlesStayDistinct(t *testing.T) {
	t.Parallel()

	stub := &stubSegmentMatcher{
		matches: []Match{
			{Kind: domain.MatchKindEvidence, Text: "first excerpt", Article: domain.Article{Title: "First", URL: "https://w/first"}, Score: 0.8},
			{Kind: domain.MatchKindEvidence, Text: "second excerpt", Article: domain.Article{Title: "Second", URL: "https://w/second"}, Score: 0.7},
		},
	}
	adapter := NewSegmentMatchAdapter(stub)

	got, err := adapter.Match(context.Background(), "segment")
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2", len(got))
	}
	if got[0].Article == nil || got[1].Article == nil {
		t.Fatal("evidence match missing article")
	}
	if got[0].Article == got[1].Article {
		t.Fatal("evidence matches share the same Article pointer")
	}
	if got[0].Article.Title != "First" || got[1].Article.Title != "Second" {
		t.Errorf("articles aliased: got %q and %q, want First and Second", got[0].Article.Title, got[1].Article.Title)
	}
}

func TestSegmentMatchAdapterEmptyMatchesNeverNil(t *testing.T) {
	t.Parallel()

	adapter := NewSegmentMatchAdapter(&stubSegmentMatcher{matches: nil})

	got, err := adapter.Match(context.Background(), "unrelated speech")
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected no matches, got %d", len(got))
	}
}

func TestSegmentMatchAdapterEmptySegmentYieldsNoMatches(t *testing.T) {
	t.Parallel()

	adapter := NewSegmentMatchAdapter(&stubSegmentMatcher{err: ErrEmptySegment})

	got, err := adapter.Match(context.Background(), "   ")
	if err != nil {
		t.Fatalf("empty segment should not error, got: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no matches for empty segment, got %d", len(got))
	}
}

func TestSegmentMatchAdapterPropagatesError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("voyage unavailable")
	adapter := NewSegmentMatchAdapter(&stubSegmentMatcher{err: wantErr})

	_, err := adapter.Match(context.Background(), "some speech")
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped %v, got %v", wantErr, err)
	}
}
