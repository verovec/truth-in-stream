package eval

import (
	"context"
	"errors"
	"testing"
)

// reversingRanker ranks documents in reverse input order; perfectRanker keeps
// them as given. With a golden case whose relevant passage sits first, the two
// produce recall 1.0 and 0.0 respectively, proving the reranked report reflects
// the ranker rather than the oracle.
type fixedOrderRanker struct {
	reverse bool
	err     error
}

func (f fixedOrderRanker) Rank(_ context.Context, _ string, documents []string) ([]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	order := make([]int, len(documents))
	for i := range order {
		if f.reverse {
			order[i] = len(documents) - 1 - i
			continue
		}
		order[i] = i
	}
	return order, nil
}

func rerankGolden() Golden {
	return Golden{Cases: []Case{
		{
			ID:        "case-1",
			Statement: "Le chomage a atteint 7,5 % au deuxieme trimestre.",
			Category:  CategoryNumberPrecision,
			Relevant:  []string{"p1"},
			Passages: []Passage{
				{ID: "p1", Text: "Le taux de chomage s'etablit a 7,5 % au T2."},
				{ID: "p2", Text: "Le taux de chomage s'etablit a 9,1 % au T2."},
				{ID: "p3", Text: "La croissance atteint 1,2 % sur l'annee."},
			},
		},
		{
			ID:           "verdict-only",
			Statement:    "Une affirmation sans axe retrieval.",
			ModelVerdict: ModelVerdict{},
		},
	}}
}

func TestRunRetrievalRerankedReflectsRanker(t *testing.T) {
	t.Parallel()
	g := rerankGolden()

	keep, err := RunRetrievalReranked(context.Background(), g, fixedOrderRanker{})
	if err != nil {
		t.Fatalf("RunRetrievalReranked(keep): %v", err)
	}
	if keep.Total != 1 || keep.OverallAt1 != 1.0 {
		t.Errorf("keep-order report = total %d R@1 %v, want 1 case at 1.0", keep.Total, keep.OverallAt1)
	}

	rev, err := RunRetrievalReranked(context.Background(), g, fixedOrderRanker{reverse: true})
	if err != nil {
		t.Fatalf("RunRetrievalReranked(reverse): %v", err)
	}
	if rev.OverallAt1 != 0.0 || rev.OverallAt3 != 1.0 {
		t.Errorf("reverse-order report = R@1 %v R@3 %v, want 0.0 and 1.0", rev.OverallAt1, rev.OverallAt3)
	}
}

func TestRunRetrievalRerankedFailsLoud(t *testing.T) {
	t.Parallel()
	g := rerankGolden()

	if _, err := RunRetrievalReranked(context.Background(), g, fixedOrderRanker{err: errors.New("api down")}); err == nil {
		t.Error("ranker error produced a report instead of failing")
	}

	bad := badOrderRanker{}
	if _, err := RunRetrievalReranked(context.Background(), g, bad); err == nil {
		t.Error("malformed ranker order produced a report instead of failing")
	}
}

type badOrderRanker struct{}

func (badOrderRanker) Rank(_ context.Context, _ string, documents []string) ([]int, error) {
	return make([]int, len(documents)), nil
}
