package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadBaseline asserts the committed baseline loads and its floors are sane
// (in [0, 1], per-category keys known), so a malformed baseline is a hard error
// rather than a vacuous gate.
func TestLoadBaseline(t *testing.T) {
	t.Parallel()
	base, err := LoadBaseline(filepath.Clean(baselinePath))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if base.Retrieval.MinOverallRecallAt1 <= 0 {
		t.Errorf("overall recall floor = %v, want a positive floor", base.Retrieval.MinOverallRecallAt1)
	}
	for _, cat := range RetrievalCategories {
		if _, ok := base.Retrieval.Categories[cat]; !ok {
			t.Errorf("baseline sets no floor for category %q", cat)
		}
	}
}

// TestBaselineVerdictFloorsMatchGateConstants binds the committed baseline's
// verdict floors to the constants the two-axis accuracy gate asserts against, so
// the single reviewed baseline file and the code cannot silently drift apart: a
// change to one without the other fails here.
func TestBaselineVerdictFloorsMatchGateConstants(t *testing.T) {
	t.Parallel()
	base, err := LoadBaseline(filepath.Clean(baselinePath))
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if base.Verdict.MinLiteralAccuracy != baselineLiteralAccuracy {
		t.Errorf("baseline literal floor %.2f != gate constant %.2f", base.Verdict.MinLiteralAccuracy, baselineLiteralAccuracy)
	}
	if base.Verdict.MinFlagAccuracy != baselineFlagAccuracy {
		t.Errorf("baseline flag floor %.2f != gate constant %.2f", base.Verdict.MinFlagAccuracy, baselineFlagAccuracy)
	}
}

// TestLoadBaselineRejectsMalformed asserts the loader rejects an out-of-range
// floor and a threshold keyed by an unknown category, the two authoring slips that
// would otherwise skew the gate.
func TestLoadBaselineRejectsMalformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "recall above one", body: `{"retrieval":{"min_overall_recall_at_1":1.5,"categories":{}}}`},
		{name: "negative accuracy", body: `{"verdict":{"min_literal_accuracy":-0.1}}`},
		{name: "unknown category", body: `{"retrieval":{"min_overall_recall_at_1":0.5,"categories":{"nonsense":0.5}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "baseline.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write temp baseline: %v", err)
			}
			if _, err := LoadBaseline(path); err == nil {
				t.Fatalf("LoadBaseline(%s) = nil error, want rejection", tc.name)
			}
		})
	}
}

// TestCheckRetrievalReportsMissedFloors asserts CheckRetrieval flags an overall
// and a per-category floor the report misses, and passes a report that meets every
// floor.
func TestCheckRetrievalReportsMissedFloors(t *testing.T) {
	t.Parallel()
	base := Baseline{Retrieval: RetrievalBaseline{
		MinOverallRecallAt1: 0.9,
		Categories:          map[string]float64{CategoryNearMiss: 1.0},
	}}
	below := RetrievalReport{
		Total:         1,
		OverallAt1:    0.5,
		ByCategory:    []CategoryRecall{{Category: CategoryNearMiss, Cases: 1, RecallAt1: 0.5}},
		byCategoryIdx: map[string]int{CategoryNearMiss: 0},
	}
	failures := base.CheckRetrieval(below)
	if !hasFailure(failures, "overall") || !hasFailure(failures, CategoryNearMiss) {
		t.Fatalf("expected overall and near-miss failures, got %v", failures)
	}

	ok := RetrievalReport{
		Total:         1,
		OverallAt1:    1.0,
		ByCategory:    []CategoryRecall{{Category: CategoryNearMiss, Cases: 1, RecallAt1: 1.0}},
		byCategoryIdx: map[string]int{CategoryNearMiss: 0},
	}
	if failures := base.CheckRetrieval(ok); len(failures) != 0 {
		t.Fatalf("expected no failures for a passing report, got %v", failures)
	}
}
