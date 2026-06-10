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
				ClaimID: "great-wall-from-space",
				Text:    "The Great Wall of China is visible from space with the naked eye.",
				Verdict: domain.VerdictContradicts,
				Sources: []domain.Source{{Title: "NASA", URL: "https://nasa.gov"}},
				Score:   0.91,
			},
			{
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
			Claim:      "The Great Wall of China is visible from space with the naked eye.",
			Verdict:    domain.VerdictContradicts,
			Sources:    []domain.Source{{Title: "NASA", URL: "https://nasa.gov"}},
			Similarity: 0.91,
		},
		{
			Claim:      "Mount Everest is the highest mountain above sea level on Earth.",
			Verdict:    domain.VerdictCorroborates,
			Sources:    nil,
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
