package eval

import (
	"path/filepath"
	"slices"
	"testing"
)

// baselinePath is the committed baseline the retrieval gate asserts against.
const baselinePath = "testdata/baseline.json"

func TestFold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"Chômage", "chomage"},
		{"DÉFICIT", "deficit"},
		{"Élisabeth Borne", "elisabeth borne"},
		{"écoulée", "ecoulee"},
		{"plain", "plain"},
	}
	for _, tc := range tests {
		if got := fold(tc.in); got != tc.want {
			t.Errorf("fold(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTokenize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "decimal and percent", in: "7,3%", want: []string{"7,3", "%"}},
		{name: "dot decimal kept", in: "l'article 49.3", want: []string{"article", "49.3"}},
		{name: "stopwords dropped", in: "le taux de chômage", want: []string{"taux", "chomage"}},
		{name: "accent folds to base", in: "chômage chomage", want: []string{"chomage", "chomage"}},
		{name: "space separates thousands", in: "1 400 euros", want: []string{"1", "400", "euros"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tokenize(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("tokenize(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRankPassagesNumberDiscrimination proves the oracle's numeric weighting does
// the near-miss job: given a claim carrying an exact figure, the passage sharing
// that figure ranks first, ahead of a same-entity passage carrying a different
// number. This is the discrimination the near-miss golden category gates.
func TestRankPassagesNumberDiscrimination(t *testing.T) {
	t.Parallel()
	passages := []Passage{
		{ID: "wrong", Text: "Le taux de chômage s'établissait à 7,5% le trimestre précédent."},
		{ID: "right", Text: "Le taux de chômage s'établit à 7,3% ce trimestre."},
		{ID: "off", Text: "Le taux d'emploi progresse de deux points."},
	}
	ranked := RankPassages("Le taux de chômage est de 7,3%.", passages)
	if len(ranked) == 0 || ranked[0] != "right" {
		t.Fatalf("ranked = %v, want the 7,3%% passage first", ranked)
	}
}

// TestRankPassagesAccentInsensitive proves accent folding lets an accent-stripped
// claim still surface the accented evidence passage first, the accented-French
// robustness the card calls out.
func TestRankPassagesAccentInsensitive(t *testing.T) {
	t.Parallel()
	passages := []Passage{
		{ID: "match", Text: "Le déficit public s'établit à 4,4% du PIB."},
		{ID: "other", Text: "La croissance ralentit selon les prévisionnistes."},
	}
	ranked := RankPassages("Le deficit public est de 4,4% du PIB.", passages)
	if ranked[0] != "match" {
		t.Fatalf("ranked = %v, want the déficit passage first despite the missing accent", ranked)
	}
}

func TestRecallAtK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		relevant []string
		ranked   []string
		k        int
		want     float64
	}{
		{name: "hit at 1", relevant: []string{"a"}, ranked: []string{"a", "b"}, k: 1, want: 1},
		{name: "miss at 1", relevant: []string{"a"}, ranked: []string{"b", "a"}, k: 1, want: 0},
		{name: "hit at 3", relevant: []string{"a"}, ranked: []string{"b", "c", "a"}, k: 3, want: 1},
		{name: "partial two targets", relevant: []string{"a", "b"}, ranked: []string{"a", "c", "d"}, k: 3, want: 0.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := recallAtK(tc.relevant, tc.ranked, tc.k); got != tc.want {
				t.Errorf("recallAtK(%v, %v, %d) = %v, want %v", tc.relevant, tc.ranked, tc.k, got, tc.want)
			}
		})
	}
}

// TestRunRetrievalCoversEveryCategory asserts the golden set exercises all five
// retrieval-stress categories with at least three cases each, so no category's
// recall floor rests on a single claim, and logs the per-category report.
func TestRunRetrievalCoversEveryCategory(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}
	rep := RunRetrieval(g)
	t.Logf("\n%s", rep.Format())
	for _, cat := range RetrievalCategories {
		cr, ok := rep.Recall(cat)
		if !ok {
			t.Errorf("golden set has no retrieval case in category %q", cat)
			continue
		}
		if cr.Cases < 3 {
			t.Errorf("category %q has %d cases, want at least 3", cat, cr.Cases)
		}
	}
}

// TestRetrievalRecallGate is the regression gate: it runs the retrieval oracle
// over the committed golden set and asserts overall and per-category recall@1 meet
// the committed baseline floors. It is deterministic and needs no network or DB.
func TestRetrievalRecallGate(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}
	base, err := LoadBaseline(filepath.Clean(baselinePath))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	rep := RunRetrieval(g)
	t.Logf("\n%s", rep.Format())
	if failures := base.CheckRetrieval(rep); len(failures) > 0 {
		t.Fatalf("retrieval recall gate failed:%s", FormatFailures(failures))
	}
}

// TestRetrievalGateHasTeeth proves the gate catches a retrieval regression: it
// mislabels every number-precision case's relevant target as a wrong-number
// distractor, so the oracle (still ranking on the claim's real figure) no longer
// puts the labeled target first, and asserts the gate reports the
// number-precision floor as missed. A gate that could not catch this is not a gate.
func TestRetrievalGateHasTeeth(t *testing.T) {
	t.Parallel()
	g, err := LoadGolden(filepath.Clean(goldenPath))
	if err != nil {
		t.Fatalf("LoadGolden: %v", err)
	}
	base, err := LoadBaseline(filepath.Clean(baselinePath))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}

	mutated := mislabelCategoryTargets(t, g, CategoryNumberPrecision)
	rep := RunRetrieval(mutated)
	failures := base.CheckRetrieval(rep)
	if !hasFailure(failures, CategoryNumberPrecision) {
		t.Fatalf("mislabeled number-precision targets did not trip the gate; failures=%v", failures)
	}
}

// mislabelCategoryTargets returns a copy of g in which every case in the given
// category has its relevant target repointed to a different passage (the last one,
// a distractor), simulating a labeling or ranking regression the gate must catch.
// It fails the test if the category has no case with a spare passage to point at,
// since the teeth check would otherwise be vacuous.
func mislabelCategoryTargets(t *testing.T, g Golden, category string) Golden {
	t.Helper()
	cases := make([]Case, len(g.Cases))
	copy(cases, g.Cases)
	mutated := 0
	for i := range cases {
		if cases[i].Category != category || len(cases[i].Passages) < 2 {
			continue
		}
		last := cases[i].Passages[len(cases[i].Passages)-1].ID
		if slices.Contains(cases[i].Relevant, last) {
			continue
		}
		cases[i].Relevant = []string{last}
		mutated++
	}
	if mutated == 0 {
		t.Fatalf("no %q case had a spare passage to mislabel; teeth check would be vacuous", category)
	}
	return Golden{About: g.About, Cases: cases}
}

// hasFailure reports whether failures names the given category.
func hasFailure(failures []RetrievalFailure, category string) bool {
	for _, f := range failures {
		if f.Category == category {
			return true
		}
	}
	return false
}
