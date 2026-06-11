package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// gatePrechecker returns a fixed decision (or error) for any text.
type gatePrechecker struct {
	decision domain.PrecheckDecision
	err      error
}

func (g gatePrechecker) Evaluate(context.Context, string) (domain.PrecheckDecision, error) {
	return g.decision, g.err
}

// gateMatcher returns fixed matches (or an error); calls records invocations so
// a test can assert the matcher is skipped for a non-checkable segment.
type gateMatcher struct {
	matches []domain.SegmentMatch
	err     error
	calls   int
}

func (g *gateMatcher) Match(context.Context, string) ([]domain.SegmentMatch, error) {
	g.calls++
	return g.matches, g.err
}

func TestGateAndMatch(t *testing.T) {
	t.Parallel()
	errPrecheck := errors.New("precheck boom")
	errMatch := errors.New("match boom")
	someMatches := []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Claim: "c", Similarity: 0.9}}

	tests := []struct {
		name         string
		prechecker   gatePrechecker
		matcher      *gateMatcher
		wantMatches  []domain.SegmentMatch
		wantDecision domain.PrecheckDecision
		wantErr      error
		wantMatched  bool
	}{
		{
			name:         "checkable segment is matched",
			prechecker:   gatePrechecker{decision: domain.Checkable()},
			matcher:      &gateMatcher{matches: someMatches},
			wantMatches:  someMatches,
			wantDecision: domain.Checkable(),
			wantMatched:  true,
		},
		{
			name:         "skipped segment never reaches the matcher",
			prechecker:   gatePrechecker{decision: domain.Skipped(domain.SkipReasonNotAClaim)},
			matcher:      &gateMatcher{matches: someMatches},
			wantMatches:  nil,
			wantDecision: domain.Skipped(domain.SkipReasonNotAClaim),
			wantMatched:  false,
		},
		{
			name:       "precheck error surfaces and the matcher is skipped",
			prechecker: gatePrechecker{err: errPrecheck},
			matcher:    &gateMatcher{matches: someMatches},
			wantErr:    errPrecheck,
		},
		{
			name:        "match error surfaces on a checkable segment",
			prechecker:  gatePrechecker{decision: domain.Checkable()},
			matcher:     &gateMatcher{err: errMatch},
			wantErr:     errMatch,
			wantMatched: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			matches, decision, err := gateAndMatch(t.Context(), tc.prechecker, tc.matcher, "some text")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if diff := cmp.Diff(tc.wantMatches, matches); diff != "" {
				t.Errorf("matches mismatch (-want +got):\n%s", diff)
			}
			if decision != tc.wantDecision {
				t.Errorf("decision = %+v, want %+v", decision, tc.wantDecision)
			}
			matched := tc.matcher.calls > 0
			if matched != tc.wantMatched {
				t.Errorf("matcher called = %v, want %v", matched, tc.wantMatched)
			}
		})
	}
}
