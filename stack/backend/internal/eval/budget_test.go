package eval

import (
	"math"
	"strings"
	"testing"
)

// budgetFixtures build a gate fixture with 1 of 4 cases in band and a stance
// fixture with 4 cases: two decided (one strong support, one weak support
// under the 0.8 trigger) and two escalations.
func budgetFixtures() (GateGolden, NLIGolden) {
	gate := GateGolden{
		BandLow:  0.35,
		BandHigh: 0.75,
		Cases: []GateCase{
			{Text: "clear accept", Label: "claim", Score: 0.9, LLMGate: true},
			{Text: "clear reject", Label: "not_claim", Score: 0.1, LLMGate: false},
			{Text: "in band", Label: "claim", Score: 0.5, LLMGate: true},
			{Text: "clear accept too", Label: "claim", Score: 0.95, LLMGate: true},
		},
	}
	stance := NLIGolden{
		EntailThreshold:     0.7,
		ContradictThreshold: 0.9,
		MinAgree:            1,
		Cases: []NLICase{
			{ID: "strong", Label: "support", Passages: []NLIPassage{{ID: "p", Text: "premise p"}}, Probs: [][]float64{{0.95, 0.03, 0.02}}},
			{ID: "weak", Label: "support", Passages: []NLIPassage{{ID: "p", Text: "premise p"}}, Probs: [][]float64{{0.75, 0.2, 0.05}}},
			{ID: "esc-1", Label: "neutral", Passages: []NLIPassage{{ID: "p", Text: "premise p"}}, Probs: [][]float64{{0.1, 0.85, 0.05}}},
			{ID: "esc-2", Label: "refute", Passages: []NLIPassage{{ID: "p", Text: "premise p"}}, Probs: [][]float64{{0.2, 0.3, 0.5}}},
		},
	}
	return gate, stance
}

func TestRunBudgetComposesStageRates(t *testing.T) {
	t.Parallel()
	gate, stance := budgetFixtures()
	rep := RunBudget(gate, stance, 0.8)

	if got, want := rep.GateRate, 0.25; got != want {
		t.Errorf("GateRate = %v, want %v", got, want)
	}
	if got, want := rep.VerdictRate, 0.5; got != want {
		t.Errorf("VerdictRate = %v, want %v", got, want)
	}
	// Only the weak support (confidence 0.75 < 0.8) triggers the second pass.
	if got, want := rep.SecondPassRate, 0.25; got != want {
		t.Errorf("SecondPassRate = %v, want %v", got, want)
	}
	if got, want := rep.Total, 1.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("Total = %v, want %v", got, want)
	}
}

func TestCheckBudgetCeiling(t *testing.T) {
	t.Parallel()
	gate, stance := budgetFixtures()
	rep := RunBudget(gate, stance, 0.8)

	t.Run("nil section skips the check", func(t *testing.T) {
		t.Parallel()
		if failures := (Baseline{}).CheckBudget(rep); failures != nil {
			t.Errorf("CheckBudget = %v, want nil without a budget baseline", failures)
		}
	})
	t.Run("within ceiling passes", func(t *testing.T) {
		t.Parallel()
		b := Baseline{Budget: &BudgetBaseline{MaxLLMCallsPerClaim: 1.2, SecondPassTriggerBelow: 0.8}}
		if failures := b.CheckBudget(rep); len(failures) != 0 {
			t.Errorf("CheckBudget = %v, want no failures", failures)
		}
	})
	t.Run("exceeded ceiling fails as a ceiling", func(t *testing.T) {
		t.Parallel()
		b := Baseline{Budget: &BudgetBaseline{MaxLLMCallsPerClaim: 0.9, SecondPassTriggerBelow: 0.8}}
		failures := b.CheckBudget(rep)
		if len(failures) != 1 || failures[0].Metric != "llm-calls-per-claim" || !failures[0].Ceiling {
			t.Fatalf("CheckBudget = %v, want the llm-calls-per-claim ceiling failure", failures)
		}
		if !strings.Contains(FormatGateFailures(failures), "above ceiling") {
			t.Error("budget failure not phrased as a ceiling")
		}
	})
}

func TestLoadBaselineValidatesBudget(t *testing.T) {
	t.Parallel()
	base := `{"verdict":{"min_literal_accuracy":1,"min_flag_accuracy":1},
		"retrieval":{"min_overall_recall_at_1":0.9},
		"gate":{"min_cascade_accuracy":0.9,"min_outside_band_llm_agreement":0.9,"max_llm_call_rate":0.2},
		"nli":{"min_decided_accuracy":0.9,"min_local_share":0.4},
		"budget":{"max_llm_calls_per_claim":0.65,"second_pass_trigger_below":0.8}}`

	t.Run("valid budget loads", func(t *testing.T) {
		t.Parallel()
		if _, err := LoadBaseline(writeGateFixture(t, base)); err != nil {
			t.Fatalf("LoadBaseline: %v", err)
		}
	})
	t.Run("non-positive ceiling rejected", func(t *testing.T) {
		t.Parallel()
		bad := strings.Replace(base, `"max_llm_calls_per_claim":0.65`, `"max_llm_calls_per_claim":0`, 1)
		if _, err := LoadBaseline(writeGateFixture(t, bad)); err == nil {
			t.Error("LoadBaseline accepted a zero ceiling")
		}
	})
	t.Run("trigger outside range rejected", func(t *testing.T) {
		t.Parallel()
		bad := strings.Replace(base, `"second_pass_trigger_below":0.8`, `"second_pass_trigger_below":1.5`, 1)
		if _, err := LoadBaseline(writeGateFixture(t, bad)); err == nil {
			t.Error("LoadBaseline accepted an out-of-range trigger")
		}
	})
	t.Run("budget without its source sections rejected", func(t *testing.T) {
		t.Parallel()
		bad := strings.Replace(base, `"nli":{"min_decided_accuracy":0.9,"min_local_share":0.4},`, ``, 1)
		if _, err := LoadBaseline(writeGateFixture(t, bad)); err == nil {
			t.Error("LoadBaseline accepted a budget without the nli section")
		}
	})
}
