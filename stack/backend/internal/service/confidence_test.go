package service

import (
	"math"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// claimMatch is a curated-claim cluster member with the given verdict and
// similarity, for confidence-scoring tests.
func claimMatch(verdict domain.Verdict, score float64) Match {
	return Match{Kind: domain.MatchKindClaim, Verdict: verdict, Score: score}
}

// evidenceMatch is a Wikipedia evidence cluster member of the given chunk kind
// and similarity, for confidence-scoring tests.
func evidenceMatch(kind domain.WikiChunkKind, score float64) Match {
	return Match{Kind: domain.MatchKindEvidence, WikiKind: kind, Score: score}
}

func TestComputeConfidence(t *testing.T) {
	t.Parallel()

	// Lead evidence weighs as much as a curated claim's similarity; body weighs
	// half, so a lead summary corroborates more strongly than buried prose.
	params := confidenceParams{clusterSize: 5, leadWeight: 1, bodyWeight: 0.5}

	tests := []struct {
		name    string
		matches []Match
		params  confidenceParams
		want    domain.Confidence
	}{
		{
			name:    "empty cluster scores zero",
			matches: nil,
			params:  params,
			want:    domain.Confidence{},
		},
		{
			name:    "single corroborating claim scores one",
			matches: []Match{claimMatch(domain.VerdictCorroborates, 0.8)},
			params:  params,
			want:    domain.Confidence{Score: 1, Supporting: 0.8, Contradicting: 0, EvidenceItems: 1},
		},
		{
			name:    "single contradicting claim scores zero",
			matches: []Match{claimMatch(domain.VerdictContradicts, 0.8)},
			params:  params,
			want:    domain.Confidence{Score: 0, Supporting: 0, Contradicting: 0.8, EvidenceItems: 1},
		},
		{
			name:    "unclear claims carry no stance and do not count",
			matches: []Match{claimMatch(domain.VerdictUnclear, 0.9), claimMatch(domain.VerdictUnclear, 0.7)},
			params:  params,
			want:    domain.Confidence{},
		},
		{
			name:    "mixed claims weight by similarity",
			matches: []Match{claimMatch(domain.VerdictCorroborates, 0.9), claimMatch(domain.VerdictContradicts, 0.3)},
			params:  params,
			want:    domain.Confidence{Score: 0.75, Supporting: 0.9, Contradicting: 0.3, EvidenceItems: 2},
		},
		{
			name:    "lead evidence corroborates at full weight",
			matches: []Match{evidenceMatch(domain.WikiChunkKindLead, 0.7)},
			params:  params,
			want:    domain.Confidence{Score: 1, Supporting: 0.7, Contradicting: 0, EvidenceItems: 1},
		},
		{
			name:    "body evidence corroborates at reduced weight",
			matches: []Match{evidenceMatch(domain.WikiChunkKindBody, 0.6)},
			params:  params,
			want:    domain.Confidence{Score: 1, Supporting: 0.3, Contradicting: 0, EvidenceItems: 1},
		},
		{
			name: "claim and evidence combine as corroboration",
			matches: []Match{
				claimMatch(domain.VerdictCorroborates, 0.8),
				evidenceMatch(domain.WikiChunkKindLead, 0.6),
			},
			params: params,
			want:   domain.Confidence{Score: 1, Supporting: 1.4, Contradicting: 0, EvidenceItems: 2},
		},
		{
			name: "cluster size caps the strongest matches",
			matches: []Match{
				claimMatch(domain.VerdictCorroborates, 0.9),
				claimMatch(domain.VerdictContradicts, 0.8),
				claimMatch(domain.VerdictCorroborates, 0.7),
			},
			params: confidenceParams{clusterSize: 2, leadWeight: 1, bodyWeight: 0.5},
			want:   domain.Confidence{Score: 0.9 / 1.7, Supporting: 0.9, Contradicting: 0.8, EvidenceItems: 2},
		},
		{
			name: "contradicted by a stronger claim scores below half",
			matches: []Match{
				claimMatch(domain.VerdictContradicts, 0.9),
				claimMatch(domain.VerdictCorroborates, 0.4),
			},
			params: params,
			want:   domain.Confidence{Score: 0.4 / 1.3, Supporting: 0.4, Contradicting: 0.9, EvidenceItems: 2},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := computeConfidence(tc.matches, tc.params)
			if !confidenceApproxEqual(got, tc.want) {
				t.Errorf("computeConfidence() = %+v, want %+v", got, tc.want)
			}
			if got.Score < 0 || got.Score > 1 {
				t.Errorf("score %v out of [0, 1]", got.Score)
			}
		})
	}
}

func TestMatchContributions(t *testing.T) {
	t.Parallel()

	params := confidenceParams{clusterSize: 5, leadWeight: 1, bodyWeight: 0.5}

	tests := []struct {
		name    string
		matches []Match
		params  confidenceParams
		want    []float64
	}{
		{
			name:    "empty cluster contributes nothing",
			matches: nil,
			params:  params,
			want:    []float64{},
		},
		{
			name:    "corroborating claim contributes its similarity",
			matches: []Match{claimMatch(domain.VerdictCorroborates, 0.8)},
			params:  params,
			want:    []float64{0.8},
		},
		{
			name:    "contradicting claim contributes its similarity (the magnitude, not the bucket)",
			matches: []Match{claimMatch(domain.VerdictContradicts, 0.8)},
			params:  params,
			want:    []float64{0.8},
		},
		{
			name:    "unclear claim contributes nothing",
			matches: []Match{claimMatch(domain.VerdictUnclear, 0.9)},
			params:  params,
			want:    []float64{0},
		},
		{
			name:    "lead evidence contributes full weight, body half",
			matches: []Match{evidenceMatch(domain.WikiChunkKindLead, 0.7), evidenceMatch(domain.WikiChunkKindBody, 0.6)},
			params:  params,
			want:    []float64{0.7, 0.3},
		},
		{
			name:    "a non-positive similarity contributes nothing",
			matches: []Match{claimMatch(domain.VerdictCorroborates, 0)},
			params:  params,
			want:    []float64{0},
		},
		{
			name: "matches beyond the cluster cap contribute nothing",
			matches: []Match{
				claimMatch(domain.VerdictCorroborates, 0.9),
				claimMatch(domain.VerdictContradicts, 0.8),
				claimMatch(domain.VerdictCorroborates, 0.7),
			},
			params: confidenceParams{clusterSize: 2, leadWeight: 1, bodyWeight: 0.5},
			want:   []float64{0.9, 0.8, 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := matchContributions(tc.matches, tc.params)
			if len(got) != len(tc.matches) {
				t.Fatalf("got %d contributions, want one per match (%d)", len(got), len(tc.matches))
			}
			for i := range tc.want {
				if math.Abs(got[i]-tc.want[i]) >= 1e-9 {
					t.Errorf("contribution[%d] = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestMatchContributionsSumMatchesScore pins the explainability invariant: the
// per-match contributions are exactly the weights computeConfidence aggregates,
// so they always sum to Supporting + Contradicting. If this drifts, the surfaced
// breakdown would no longer explain the score it sits next to.
func TestMatchContributionsSumMatchesScore(t *testing.T) {
	t.Parallel()

	params := confidenceParams{clusterSize: 5, leadWeight: 1, bodyWeight: 0.5}
	matches := []Match{
		claimMatch(domain.VerdictCorroborates, 0.9),
		claimMatch(domain.VerdictContradicts, 0.4),
		evidenceMatch(domain.WikiChunkKindBody, 0.6),
		claimMatch(domain.VerdictUnclear, 0.5),
	}

	conf := computeConfidence(matches, params)
	contribs := matchContributions(matches, params)

	var sum float64
	for _, c := range contribs {
		sum += c
	}
	if math.Abs(sum-(conf.Supporting+conf.Contradicting)) >= 1e-9 {
		t.Errorf("contributions sum to %v, want Supporting+Contradicting = %v", sum, conf.Supporting+conf.Contradicting)
	}
}

// confidenceApproxEqual compares two Confidence values within a float tolerance,
// so a clean expected fraction need not be transcribed to full precision.
func confidenceApproxEqual(a, b domain.Confidence) bool {
	const eps = 1e-9
	return math.Abs(a.Score-b.Score) < eps &&
		math.Abs(a.Supporting-b.Supporting) < eps &&
		math.Abs(a.Contradicting-b.Contradicting) < eps &&
		a.EvidenceItems == b.EvidenceItems
}
