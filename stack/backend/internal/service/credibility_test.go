package service

import (
	"math"
	"testing"
)

const scoreEpsilon = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < scoreEpsilon
}

// TestSpeakerCredibilityScore drives the pure Beta-Binomial aggregator through the
// behaviors the design pins down: a neutral empty score, a single credible claim
// shrunk well below 100%, symmetric movement for disputed, convergence with
// volume, unverifiable touching only the tally, confidence-weighting, and an
// unknown state being ignored.
func TestSpeakerCredibilityScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prior   float64
		observe []struct {
			state      string
			confidence float64
		}
		wantScore        float64
		wantCredible     int
		wantDisputed     int
		wantUnverifiable int
	}{
		{
			name:      "no claims is neutral one half",
			prior:     4,
			wantScore: 0.5,
		},
		{
			name:  "one full-confidence credible shrinks to 0.6 not 1.0",
			prior: 4,
			observe: []struct {
				state      string
				confidence float64
			}{{VerdictCredible, 1.0}},
			wantScore:    0.6,
			wantCredible: 1,
		},
		{
			name:  "one full-confidence disputed shrinks to 0.4",
			prior: 4,
			observe: []struct {
				state      string
				confidence float64
			}{{VerdictDisputed, 1.0}},
			wantScore:    0.4,
			wantDisputed: 1,
		},
		{
			name:  "volume of credible converges above the shrunk single-claim value",
			prior: 4,
			observe: []struct {
				state      string
				confidence float64
			}{
				{VerdictCredible, 1.0},
				{VerdictCredible, 1.0},
				{VerdictCredible, 1.0},
				{VerdictCredible, 1.0},
				{VerdictCredible, 1.0},
				{VerdictCredible, 1.0},
			},
			// (2 + 6) / (4 + 6) = 0.8, sharper than the 0.6 of a single claim.
			wantScore:    0.8,
			wantCredible: 6,
		},
		{
			name:  "unverifiable moves only the tally, never the score",
			prior: 4,
			observe: []struct {
				state      string
				confidence float64
			}{{VerdictUnverifiable, 0.9}, {VerdictUnverifiable, 0.2}},
			wantScore:        0.5,
			wantUnverifiable: 2,
		},
		{
			name:  "confidence-weights the observation",
			prior: 4,
			observe: []struct {
				state      string
				confidence float64
			}{{VerdictCredible, 0.5}},
			// (2 + 0.5) / (4 + 0.5) = 0.5555...
			wantScore:    2.5 / 4.5,
			wantCredible: 1,
		},
		{
			name:  "larger prior strength moves the score more slowly",
			prior: 10,
			observe: []struct {
				state      string
				confidence float64
			}{{VerdictCredible, 1.0}},
			// (5 + 1) / (10 + 1) = 0.5454..., closer to neutral than k=4's 0.6.
			wantScore:    6.0 / 11.0,
			wantCredible: 1,
		},
		{
			name:  "mixed credible and disputed with unverifiable excluded",
			prior: 4,
			observe: []struct {
				state      string
				confidence float64
			}{
				{VerdictCredible, 1.0},
				{VerdictCredible, 1.0},
				{VerdictDisputed, 1.0},
				{VerdictUnverifiable, 1.0},
			},
			// (2 + 2) / (4 + 2 + 1) = 4/7.
			wantScore:        4.0 / 7.0,
			wantCredible:     2,
			wantDisputed:     1,
			wantUnverifiable: 1,
		},
		{
			name:  "unknown state is ignored entirely",
			prior: 4,
			observe: []struct {
				state      string
				confidence float64
			}{{"not_a_verdict", 1.0}, {VerdictCredible, 1.0}},
			wantScore:    0.6,
			wantCredible: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := newSpeakerCredibility(tc.prior)
			last := sc.snapshot()
			for _, o := range tc.observe {
				last = sc.observe(o.state, o.confidence)
			}
			if !approxEqual(last.Score, tc.wantScore) {
				t.Errorf("score = %v, want %v", last.Score, tc.wantScore)
			}
			if last.Credible != tc.wantCredible {
				t.Errorf("credible = %d, want %d", last.Credible, tc.wantCredible)
			}
			if last.Disputed != tc.wantDisputed {
				t.Errorf("disputed = %d, want %d", last.Disputed, tc.wantDisputed)
			}
			if last.Unverifiable != tc.wantUnverifiable {
				t.Errorf("unverifiable = %d, want %d", last.Unverifiable, tc.wantUnverifiable)
			}
		})
	}
}

// TestSpeakerCredibilityClampsConfidence asserts an out-of-range or NaN confidence
// is bounded to [0,1] before it weights an observation, so a stray value cannot
// distort the score.
func TestSpeakerCredibilityClampsConfidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		confidence float64
		wantScore  float64
	}{
		{name: "above one weights as one", confidence: 1.5, wantScore: 0.6},
		{name: "below zero weights as zero", confidence: -0.4, wantScore: 0.5},
		{name: "nan weights as zero", confidence: math.NaN(), wantScore: 0.5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sc := newSpeakerCredibility(4)
			got := sc.observe(VerdictCredible, tc.confidence)
			if !approxEqual(got.Score, tc.wantScore) {
				t.Errorf("score = %v, want %v", got.Score, tc.wantScore)
			}
		})
	}
}

// TestSpeakerCredibilityFramingIsOrthogonal asserts the political path's second
// axis: a manipulation flag bumps the misleading-framing tally without moving the
// score or any credibility tally, and a flagged-but-accurate claim moves the
// score up (via observe) and the framing tally (via observeFraming) independently.
func TestSpeakerCredibilityFramingIsOrthogonal(t *testing.T) {
	t.Parallel()

	t.Run("flag alone moves only the framing tally", func(t *testing.T) {
		t.Parallel()
		sc := newSpeakerCredibility(4)
		got := sc.observeFraming()
		if !approxEqual(got.Score, 0.5) {
			t.Errorf("score = %v, want neutral 0.5 (framing must not move the score)", got.Score)
		}
		if got.Credible != 0 || got.Disputed != 0 || got.Unverifiable != 0 {
			t.Errorf("credibility tallies = %d/%d/%d, want all zero", got.Credible, got.Disputed, got.Unverifiable)
		}
		if got.MisleadingFraming != 1 {
			t.Errorf("misleading framing = %d, want 1", got.MisleadingFraming)
		}
	})

	t.Run("accurate-but-flagged moves the score up and the framing tally", func(t *testing.T) {
		t.Parallel()
		sc := newSpeakerCredibility(4)
		sc.observe(VerdictCredible, 1.0)
		got := sc.observeFraming()
		// (2 + 1) / (4 + 1) = 0.6: the literal-accurate claim moved the score; the
		// flag is independent.
		if !approxEqual(got.Score, 0.6) {
			t.Errorf("score = %v, want 0.6", got.Score)
		}
		if got.Credible != 1 {
			t.Errorf("credible = %d, want 1", got.Credible)
		}
		if got.MisleadingFraming != 1 {
			t.Errorf("misleading framing = %d, want 1", got.MisleadingFraming)
		}
	})

	t.Run("framing tally accumulates across claims", func(t *testing.T) {
		t.Parallel()
		sc := newSpeakerCredibility(4)
		sc.observeFraming()
		got := sc.observeFraming()
		if got.MisleadingFraming != 2 {
			t.Errorf("misleading framing = %d, want 2", got.MisleadingFraming)
		}
	})
}

// TestNewSpeakerCredibilityFallsBackOnNonPositivePrior asserts a non-positive prior
// strength falls back to the default rather than producing a divide-by-zero score.
func TestNewSpeakerCredibilityFallsBackOnNonPositivePrior(t *testing.T) {
	t.Parallel()
	for _, prior := range []float64{0, -1} {
		sc := newSpeakerCredibility(prior)
		if got := sc.snapshot().Score; !approxEqual(got, 0.5) {
			t.Errorf("prior %v: empty score = %v, want neutral 0.5", prior, got)
		}
	}
}
