package service

import (
	"math"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func recencyParams(halfLife time.Duration, now time.Time) confidenceParams {
	return confidenceParams{clusterSize: 5, leadWeight: 1, bodyWeight: 0.6, recencyHalfLife: halfLife, now: now}
}

func datedEvidenceMatch(score float64, publishedAt *time.Time) Match {
	return Match{Kind: domain.MatchKindEvidence, WikiKind: domain.EvidenceKindLead, Score: score, PublishedAt: publishedAt}
}

func TestRecencyDecayHalvesDatedEvidencePerHalfLife(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	halfLife := 365 * 24 * time.Hour
	oneHalfLifeAgo := now.Add(-halfLife)
	twoHalfLivesAgo := now.Add(-2 * halfLife)

	p := recencyParams(halfLife, now)
	one := matchWeight(datedEvidenceMatch(0.8, &oneHalfLifeAgo), p)
	if math.Abs(one-0.4) > 1e-9 {
		t.Errorf("weight at one half-life = %v, want 0.4", one)
	}
	two := matchWeight(datedEvidenceMatch(0.8, &twoHalfLivesAgo), p)
	if math.Abs(two-0.2) > 1e-9 {
		t.Errorf("weight at two half-lives = %v, want 0.2", two)
	}
}

func TestRecencyDecayNeverTouchesUndatedFutureOrDisabled(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	halfLife := 365 * 24 * time.Hour
	old := now.Add(-3 * halfLife)
	future := now.Add(24 * time.Hour)

	if w := matchWeight(datedEvidenceMatch(0.8, nil), recencyParams(halfLife, now)); w != 0.8 {
		t.Errorf("undated evidence weight = %v, want 0.8 (never decayed)", w)
	}
	if w := matchWeight(datedEvidenceMatch(0.8, &future), recencyParams(halfLife, now)); w != 0.8 {
		t.Errorf("future-dated evidence weight = %v, want 0.8 (no decay forward)", w)
	}
	if w := matchWeight(datedEvidenceMatch(0.8, &old), recencyParams(0, now)); w != 0.8 {
		t.Errorf("weight with decay off = %v, want 0.8 (default scoring unchanged)", w)
	}
	claim := Match{Kind: domain.MatchKindClaim, Verdict: domain.VerdictCorroborates, Score: 0.9, PublishedAt: &old}
	if w := matchWeight(claim, recencyParams(halfLife, now)); w != 0.9 {
		t.Errorf("curated claim weight = %v, want 0.9 (decay is evidence-only)", w)
	}
}

func TestRecencyDecayKeepsScoreAndContributionsConsistent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	halfLife := 30 * 24 * time.Hour
	dated := now.Add(-halfLife)
	matches := []Match{
		datedEvidenceMatch(0.8, &dated),
		datedEvidenceMatch(0.6, nil),
	}
	p := recencyParams(halfLife, now)

	conf := computeConfidence(matches, p)
	contribs := matchContributions(matches, p)
	var sum float64
	for _, c := range contribs {
		sum += c
	}
	if math.Abs(conf.Supporting-sum) > 1e-9 {
		t.Errorf("supporting %v != contributions sum %v; score and surfaced weights drifted", conf.Supporting, sum)
	}
}
