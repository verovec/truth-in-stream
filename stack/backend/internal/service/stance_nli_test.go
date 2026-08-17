package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeStanceScorer returns the stances keyed by claim text and records every
// call, so a test can assert which claims were scored and against how many
// passages. A nil entry for a claim yields err.
type fakeStanceScorer struct {
	byClaim map[string][]StanceResult
	err     error
	calls   []struct {
		claim    string
		passages []string
	}
}

func (f *fakeStanceScorer) ScoreStances(_ context.Context, claim string, passages []string) ([]StanceResult, error) {
	f.calls = append(f.calls, struct {
		claim    string
		passages []string
	}{claim, passages})
	if f.err != nil {
		return nil, f.err
	}
	return f.byClaim[claim], nil
}

func entail(p float64) StanceResult     { return StanceResult{Entailment: p, Neutral: 1 - p} }
func contradict(p float64) StanceResult { return StanceResult{Contradiction: p, Neutral: 1 - p} }
func neutral() StanceResult             { return StanceResult{Neutral: 1} }

func stanceMatches(ids ...string) []domain.SegmentMatch {
	matches := make([]domain.SegmentMatch, len(ids))
	for i, id := range ids {
		matches[i] = domain.SegmentMatch{EvidenceID: id, Claim: "passage " + id, Similarity: 0.9}
	}
	return matches
}

func stanceTestPath(t *testing.T, scorer EvidenceStanceScorer, minAgree, maxPassages int, verifier *fakeVerifier) *VerifyPath {
	t.Helper()
	vp, err := NewVerifyPath(VerifyPathConfig{
		Decomposer: fakeDecomposer{}, Matcher: liveMatcher{}, Verifier: verifier,
		FastTau: 0.85, VerifyConcurrency: 1, FastDeadline: time.Second, VerifyDeadline: time.Second,
		Logger: discardLogger(),
		NLIStance: &StanceConfig{
			Scorer:              scorer,
			EntailThreshold:     0.7,
			ContradictThreshold: 0.9,
			MinAgree:            minAgree,
			MaxPassages:         maxPassages,
		},
	})
	if err != nil {
		t.Fatalf("NewVerifyPath: %v", err)
	}
	return vp
}

func TestStanceResolveRouting(t *testing.T) {
	t.Parallel()
	const claim = "Le chômage a baissé de deux points."

	tests := []struct {
		name        string
		stances     []StanceResult
		minAgree    int
		wantVerdict string
		wantLocal   bool
		wantEscal   bool
		wantCites   int
	}{
		{
			name:        "clear support decides credible locally",
			stances:     []StanceResult{entail(0.95), neutral()},
			minAgree:    1,
			wantVerdict: VerdictCredible,
			wantLocal:   true,
			wantCites:   1,
		},
		{
			name:        "clear contradiction decides disputed locally",
			stances:     []StanceResult{contradict(0.97), neutral()},
			minAgree:    1,
			wantVerdict: VerdictDisputed,
			wantLocal:   true,
			wantCites:   1,
		},
		{
			name:        "two agreeing passages both cited",
			stances:     []StanceResult{entail(0.9), entail(0.8)},
			minAgree:    1,
			wantVerdict: VerdictCredible,
			wantLocal:   true,
			wantCites:   2,
		},
		{
			name:      "mixed signals escalate",
			stances:   []StanceResult{entail(0.95), contradict(0.95)},
			minAgree:  1,
			wantEscal: true,
		},
		{
			name:      "all neutral escalates",
			stances:   []StanceResult{neutral(), neutral()},
			minAgree:  1,
			wantEscal: true,
		},
		{
			name:      "below both thresholds escalates",
			stances:   []StanceResult{entail(0.6), contradict(0.85)},
			minAgree:  1,
			wantEscal: true,
		},
		{
			name:      "min agree unmet escalates",
			stances:   []StanceResult{entail(0.95), neutral()},
			minAgree:  2,
			wantEscal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{claim: {
				Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.8,
				Citations: []EvidenceCitation{{EvidenceID: "e1", QuotedSpan: "passage e1"}},
			}}}
			scorer := &fakeStanceScorer{byClaim: map[string][]StanceResult{claim: tt.stances}}
			vp := stanceTestPath(t, scorer, tt.minAgree, 6, verifier)

			verdict, shed, err := vp.verifyClaim(context.Background(), claim, stanceMatches("e1", "e2"))
			if err != nil || shed {
				t.Fatalf("verifyClaim: verdict=%v shed=%v err=%v", verdict, shed, err)
			}
			if tt.wantEscal {
				if len(verifier.calls) != 1 {
					t.Fatalf("verifier called %d times, want 1 (escalation)", len(verifier.calls))
				}
				if verdict.DecidedLocally {
					t.Error("escalated verdict marked DecidedLocally")
				}
				return
			}
			if len(verifier.calls) != 0 {
				t.Fatalf("verifier called %d times, want 0 for a local decision", len(verifier.calls))
			}
			if verdict.Verdict != tt.wantVerdict || verdict.Basis != BasisEvidence {
				t.Errorf("verdict = %s/%s, want %s/evidence", verdict.Verdict, verdict.Basis, tt.wantVerdict)
			}
			if !verdict.DecidedLocally {
				t.Error("local verdict not marked DecidedLocally")
			}
			if len(verdict.Citations) != tt.wantCites {
				t.Errorf("citations = %d, want %d", len(verdict.Citations), tt.wantCites)
			}
		})
	}
}

func TestStanceResolveConfidenceIsMeanOfAgreeing(t *testing.T) {
	t.Parallel()
	const claim = "La dette dépasse trois mille milliards."
	scorer := &fakeStanceScorer{byClaim: map[string][]StanceResult{claim: {entail(0.9), entail(0.8), neutral()}}}
	vp := stanceTestPath(t, scorer, 1, 6, &fakeVerifier{})

	verdict, _, err := vp.verifyClaim(context.Background(), claim, stanceMatches("e1", "e2", "e3"))
	if err != nil {
		t.Fatalf("verifyClaim: %v", err)
	}
	if want := (0.9 + 0.8) / 2; math.Abs(verdict.Confidence-want) > 1e-9 {
		t.Errorf("confidence = %v, want %v", verdict.Confidence, want)
	}
}

func TestStanceResolveNegationPairGetsOppositeVerdicts(t *testing.T) {
	t.Parallel()
	const claim = "Le chômage a baissé."
	const negated = "Le chômage n'a pas baissé."
	scorer := &fakeStanceScorer{byClaim: map[string][]StanceResult{
		claim:   {entail(0.95)},
		negated: {contradict(0.95)},
	}}
	vp := stanceTestPath(t, scorer, 1, 6, &fakeVerifier{})

	forClaim, _, err := vp.verifyClaim(context.Background(), claim, stanceMatches("e1"))
	if err != nil {
		t.Fatalf("verifyClaim(claim): %v", err)
	}
	forNegated, _, err := vp.verifyClaim(context.Background(), negated, stanceMatches("e1"))
	if err != nil {
		t.Fatalf("verifyClaim(negated): %v", err)
	}
	if forClaim.Verdict == forNegated.Verdict {
		t.Errorf("claim and its negation both got %s from the same passage", forClaim.Verdict)
	}
}

func TestStanceResolveFailsOpen(t *testing.T) {
	t.Parallel()
	const claim = "Une affirmation à juger."
	llmVerdict := ClaimVerdict{
		Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.7,
		Citations: []EvidenceCitation{{EvidenceID: "e1", QuotedSpan: "passage e1"}},
	}

	t.Run("scorer error escalates to the verifier", func(t *testing.T) {
		t.Parallel()
		verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{claim: llmVerdict}}
		scorer := &fakeStanceScorer{err: errors.New("model down")}
		vp := stanceTestPath(t, scorer, 1, 6, verifier)

		verdict, _, err := vp.verifyClaim(context.Background(), claim, stanceMatches("e1"))
		if err != nil {
			t.Fatalf("verifyClaim: %v", err)
		}
		if len(verifier.calls) != 1 || verdict.Verdict != VerdictDisputed || verdict.DecidedLocally {
			t.Errorf("expected the verifier's verdict on scorer failure, got %+v after %d verifier calls", verdict, len(verifier.calls))
		}
	})
	t.Run("mismatched stance count escalates", func(t *testing.T) {
		t.Parallel()
		verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{claim: llmVerdict}}
		scorer := &fakeStanceScorer{byClaim: map[string][]StanceResult{claim: {entail(0.95)}}}
		vp := stanceTestPath(t, scorer, 1, 6, verifier)

		if _, _, err := vp.verifyClaim(context.Background(), claim, stanceMatches("e1", "e2")); err != nil {
			t.Fatalf("verifyClaim: %v", err)
		}
		if len(verifier.calls) != 1 {
			t.Errorf("verifier called %d times, want 1 after a mismatched stance count", len(verifier.calls))
		}
	})
	t.Run("no citable matches skip the scorer", func(t *testing.T) {
		t.Parallel()
		verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{claim: llmVerdict}}
		scorer := &fakeStanceScorer{}
		vp := stanceTestPath(t, scorer, 1, 6, verifier)

		matches := []domain.SegmentMatch{{Claim: "no evidence id", Similarity: 0.9}}
		if _, _, err := vp.verifyClaim(context.Background(), claim, matches); err != nil {
			t.Fatalf("verifyClaim: %v", err)
		}
		if len(scorer.calls) != 0 {
			t.Errorf("scorer called %d times, want 0 without citable matches", len(scorer.calls))
		}
		if len(verifier.calls) != 1 {
			t.Errorf("verifier called %d times, want 1", len(verifier.calls))
		}
	})
}

func TestStanceMaxPassagesCapsScoring(t *testing.T) {
	t.Parallel()
	const claim = "Une affirmation avec beaucoup de sources."
	scorer := &fakeStanceScorer{byClaim: map[string][]StanceResult{claim: {entail(0.95), entail(0.9)}}}
	vp := stanceTestPath(t, scorer, 1, 2, &fakeVerifier{})

	if _, _, err := vp.verifyClaim(context.Background(), claim, stanceMatches("e1", "e2", "e3", "e4")); err != nil {
		t.Fatalf("verifyClaim: %v", err)
	}
	if len(scorer.calls) != 1 || len(scorer.calls[0].passages) != 2 {
		t.Fatalf("scorer saw %v, want one call with 2 passages", scorer.calls)
	}
}

func TestNewVerifyPathValidatesStanceConfig(t *testing.T) {
	t.Parallel()
	base := func() VerifyPathConfig {
		return VerifyPathConfig{
			Decomposer: fakeDecomposer{}, Matcher: liveMatcher{}, Verifier: &fakeVerifier{},
			FastTau: 0.85, VerifyConcurrency: 1, FastDeadline: time.Second, VerifyDeadline: time.Second,
		}
	}
	valid := &StanceConfig{Scorer: &fakeStanceScorer{}, EntailThreshold: 0.7, ContradictThreshold: 0.9, MinAgree: 1, MaxPassages: 6}

	tests := []struct {
		name    string
		mutate  func(*StanceConfig)
		wantErr bool
	}{
		{name: "valid", mutate: func(*StanceConfig) {}},
		{name: "missing scorer", mutate: func(c *StanceConfig) { c.Scorer = nil }, wantErr: true},
		{name: "zero entail threshold", mutate: func(c *StanceConfig) { c.EntailThreshold = 0 }, wantErr: true},
		{name: "contradict threshold above one", mutate: func(c *StanceConfig) { c.ContradictThreshold = 1.5 }, wantErr: true},
		{name: "zero min agree", mutate: func(c *StanceConfig) { c.MinAgree = 0 }, wantErr: true},
		{name: "zero max passages", mutate: func(c *StanceConfig) { c.MaxPassages = 0 }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			stance := *valid
			tt.mutate(&stance)
			cfg.NLIStance = &stance
			_, err := NewVerifyPath(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewVerifyPath error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
