package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNLIFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nli_golden.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadNLIGoldenValidates(t *testing.T) {
	t.Parallel()
	valid := `{"temperature":1.86,"entail_threshold":0.7,"contradict_threshold":0.9,"min_agree":1,
		"cases":[{"id":"a","claim":"c","label":"support","passage_ids":["p1"],"probs":[[0.9,0.05,0.05]]}]}`

	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "valid", content: valid},
		{name: "no cases", content: `{"entail_threshold":0.7,"contradict_threshold":0.9,"min_agree":1,"cases":[]}`, wantErr: true},
		{name: "unknown label", content: strings.Replace(valid, `"support"`, `"maybe"`, 1), wantErr: true},
		{name: "probability out of range", content: strings.Replace(valid, "0.9,0.05,0.05", "1.9,0.05,0.05", 1), wantErr: true},
		{name: "misaligned prob rows", content: strings.Replace(valid, `"passage_ids":["p1"]`, `"passage_ids":["p1","p2"]`, 1), wantErr: true},
		{name: "two-wide prob row", content: strings.Replace(valid, "[[0.9,0.05,0.05]]", "[[0.9,0.1]]", 1), wantErr: true},
		{name: "zero min agree", content: strings.Replace(valid, `"min_agree":1`, `"min_agree":0`, 1), wantErr: true},
		{name: "inverted thresholds ok but out of range rejected", content: strings.Replace(valid, `"entail_threshold":0.7`, `"entail_threshold":1.7`, 1), wantErr: true},
		{
			name: "dangling negation link",
			content: `{"entail_threshold":0.7,"contradict_threshold":0.9,"min_agree":1,
				"cases":[{"id":"a","claim":"c","label":"support","negation_of":"ghost","passage_ids":["p1"],"probs":[[0.9,0.05,0.05]]}]}`,
			wantErr: true,
		},
		{
			name: "duplicate id",
			content: `{"entail_threshold":0.7,"contradict_threshold":0.9,"min_agree":1,"cases":[
				{"id":"a","claim":"c","label":"support","passage_ids":["p1"],"probs":[[0.9,0.05,0.05]]},
				{"id":"a","claim":"c2","label":"refute","passage_ids":["p2"],"probs":[[0.05,0.05,0.9]]}]}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadNLIGolden(writeNLIFixture(t, tt.content))
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadNLIGolden error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// nliFixture builds a fixture with known metrics: four decided cases (three
// correct, one wrong refute), two escalations, and one negation pair that
// violates the invariant.
func nliFixture() NLIGolden {
	return NLIGolden{
		EntailThreshold:     0.7,
		ContradictThreshold: 0.9,
		MinAgree:            1,
		Cases: []NLICase{
			{ID: "s1", Label: "support", PassageIDs: []string{"p"}, Probs: [][]float64{{0.9, 0.05, 0.05}}},
			{ID: "r1", Label: "refute", PassageIDs: []string{"p"}, Probs: [][]float64{{0.02, 0.03, 0.95}}},
			{ID: "wrong", Label: "support", PassageIDs: []string{"p"}, Probs: [][]float64{{0.02, 0.03, 0.95}}},
			{ID: "esc-mixed", Label: "support", PassageIDs: []string{"a", "b"}, Probs: [][]float64{{0.9, 0.05, 0.05}, {0.02, 0.03, 0.95}}},
			{ID: "esc-neutral", Label: "neutral", PassageIDs: []string{"p"}, Probs: [][]float64{{0.1, 0.85, 0.05}}},
			{ID: "neg-a", Label: "support", PassageIDs: []string{"p"}, Probs: [][]float64{{0.92, 0.04, 0.04}}},
			{ID: "neg-b", Label: "refute", NegationOf: "neg-a", PassageIDs: []string{"p"}, Probs: [][]float64{{0.91, 0.05, 0.04}}},
		},
	}
}

func TestRunNLIRecomputesMetrics(t *testing.T) {
	t.Parallel()
	rep := RunNLI(nliFixture())

	if rep.Cases != 7 || rep.Decided != 5 {
		t.Fatalf("cases/decided = %d/%d, want 7/5", rep.Cases, rep.Decided)
	}
	if got, want := rep.LocalShare, 5.0/7.0; got != want {
		t.Errorf("LocalShare = %v, want %v", got, want)
	}
	// Three of the five decided cases carry the right label: s1, r1, and
	// neg-a; "wrong" decides refute against a support label, and neg-b
	// decides support against its refute label.
	if got, want := rep.DecidedAccuracy, 3.0/5.0; got != want {
		t.Errorf("DecidedAccuracy = %v, want %v", got, want)
	}
	if len(rep.NegationViolations) != 1 {
		t.Fatalf("NegationViolations = %v, want exactly one", rep.NegationViolations)
	}
	if len(rep.Wrong) != 2 {
		t.Errorf("Wrong = %v, want the mislabeled refute and the negation twin", rep.Wrong)
	}
}

func TestCheckNLIBounds(t *testing.T) {
	t.Parallel()
	rep := RunNLI(nliFixture())

	t.Run("nil section skips the check", func(t *testing.T) {
		t.Parallel()
		if failures := (Baseline{}).CheckNLI(rep); failures != nil {
			t.Errorf("CheckNLI = %v, want nil without an nli baseline", failures)
		}
	})
	t.Run("negation violation always fails when gated", func(t *testing.T) {
		t.Parallel()
		b := Baseline{NLI: &NLIBaseline{MinDecidedAccuracy: 0.5, MinLocalShare: 0.5}}
		failures := b.CheckNLI(rep)
		if len(failures) != 1 || failures[0].Metric != "nli-negation-violations" || !failures[0].Ceiling {
			t.Fatalf("CheckNLI = %v, want exactly the negation-violation ceiling", failures)
		}
	})
	t.Run("each floor can fail in order", func(t *testing.T) {
		t.Parallel()
		b := Baseline{NLI: &NLIBaseline{MinDecidedAccuracy: 0.95, MinLocalShare: 0.9}}
		failures := b.CheckNLI(rep)
		want := []string{"nli-decided-accuracy", "nli-local-share", "nli-negation-violations"}
		if len(failures) != len(want) {
			t.Fatalf("CheckNLI returned %d failures, want %d: %v", len(failures), len(want), failures)
		}
		for i, f := range failures {
			if f.Metric != want[i] {
				t.Errorf("failure %d = %q, want %q", i, f.Metric, want[i])
			}
		}
	})
}
