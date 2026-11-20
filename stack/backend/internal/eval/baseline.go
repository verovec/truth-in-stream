package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// Baseline is the committed floor the eval gates against: the retrieval oracle's
// minimum recall (overall and per category) plus the two-axis verdict accuracy
// floors. It is a single reviewed file (testdata/baseline.json) so any move of the
// gate - up when quality ratchets, or a deliberate relaxation - is an explicit,
// diffable change in the PR that makes it, never a silent drift. The retrieval
// numbers gate this card's recall eval; the verdict numbers mirror the two-axis
// accuracy floors the verify-path gate already enforces, kept here so the whole
// baseline lives in one place and a drift-guard test binds them to the code.
type Baseline struct {
	About     string            `json:"_about"`
	Verdict   VerdictBaseline   `json:"verdict"`
	Retrieval RetrievalBaseline `json:"retrieval"`
	// Gate is the local check-worthiness gate floor (VER-225). It is additive
	// and optional: a baseline without the key skips the gate check.
	Gate *GateBaseline `json:"gate,omitempty"`
	// NLI is the local stance stage floor (VER-228). Additive and optional
	// like Gate.
	NLI *NLIBaseline `json:"nli,omitempty"`
	// Budget is the generative-cost ceiling (VER-230). Additive and optional;
	// checking it requires both the Gate and NLI fixtures.
	Budget *BudgetBaseline `json:"budget,omitempty"`
}

// VerdictBaseline is the two-axis accuracy floor: the minimum fraction of cases
// whose literal verdict, and whose surviving flag set, must match their labels.
type VerdictBaseline struct {
	MinLiteralAccuracy float64 `json:"min_literal_accuracy"`
	MinFlagAccuracy    float64 `json:"min_flag_accuracy"`
}

// RetrievalBaseline is the retrieval recall floor: the minimum overall recall@1
// and a per-category minimum recall@1 keyed by RetrievalCategories value. Recall@1
// is the strict cut-off the gate asserts (the true target must rank first over its
// distractors); recall@3 is reported but not gated, so a change that only reorders
// within the top three is visible without failing the build.
type RetrievalBaseline struct {
	MinOverallRecallAt1 float64            `json:"min_overall_recall_at_1"`
	Categories          map[string]float64 `json:"categories"`
}

// LoadBaseline reads and validates the committed baseline file. It fails on a
// missing file, malformed JSON, an accuracy or recall outside [0, 1], or a
// per-category threshold keyed by an unknown category, so a baseline authoring
// slip is a hard error rather than a gate that passes vacuously.
func LoadBaseline(path string) (Baseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Baseline{}, fmt.Errorf("eval: read baseline: %w", err)
	}
	var b Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return Baseline{}, fmt.Errorf("eval: decode baseline: %w", err)
	}
	for name, v := range map[string]float64{
		"verdict.min_literal_accuracy":      b.Verdict.MinLiteralAccuracy,
		"verdict.min_flag_accuracy":         b.Verdict.MinFlagAccuracy,
		"retrieval.min_overall_recall_at_1": b.Retrieval.MinOverallRecallAt1,
	} {
		if v < 0 || v > 1 {
			return Baseline{}, fmt.Errorf("eval: baseline %s %.4f is outside [0, 1]", name, v)
		}
	}
	if b.Gate != nil {
		for name, v := range map[string]float64{
			"gate.min_cascade_accuracy":           b.Gate.MinCascadeAccuracy,
			"gate.min_outside_band_llm_agreement": b.Gate.MinOutsideBandLLMAgreement,
			"gate.max_llm_call_rate":              b.Gate.MaxLLMCallRate,
		} {
			if v < 0 || v > 1 {
				return Baseline{}, fmt.Errorf("eval: baseline %s %.4f is outside [0, 1]", name, v)
			}
		}
	}
	if b.NLI != nil {
		for name, v := range map[string]float64{
			"nli.min_decided_accuracy": b.NLI.MinDecidedAccuracy,
			"nli.min_local_share":      b.NLI.MinLocalShare,
		} {
			if v < 0 || v > 1 {
				return Baseline{}, fmt.Errorf("eval: baseline %s %.4f is outside [0, 1]", name, v)
			}
		}
	}
	if b.Budget != nil {
		if b.Budget.MaxLLMCallsPerClaim <= 0 {
			return Baseline{}, fmt.Errorf("eval: baseline budget.max_llm_calls_per_claim %.4f must be positive", b.Budget.MaxLLMCallsPerClaim)
		}
		if b.Budget.SecondPassTriggerBelow < 0 || b.Budget.SecondPassTriggerBelow > 1 {
			return Baseline{}, fmt.Errorf("eval: baseline budget.second_pass_trigger_below %.4f is outside [0, 1]", b.Budget.SecondPassTriggerBelow)
		}
		if b.Gate == nil || b.NLI == nil {
			return Baseline{}, fmt.Errorf("eval: baseline budget requires the gate and nli sections it is composed from")
		}
	}
	for cat, v := range b.Retrieval.Categories {
		if !validCategory(cat) || cat == "" {
			return Baseline{}, fmt.Errorf("eval: baseline threshold for unknown category %q", cat)
		}
		if v < 0 || v > 1 {
			return Baseline{}, fmt.Errorf("eval: baseline recall for category %q %.4f is outside [0, 1]", cat, v)
		}
	}
	return b, nil
}

// RetrievalFailure is one category (or the overall aggregate) whose measured
// recall@1 fell below its committed floor, the unit CheckRetrieval reports so the
// gate names exactly which query class regressed and by how much.
type RetrievalFailure struct {
	Category string
	Got      float64
	Want     float64
}

// CheckRetrieval compares a retrieval report against the baseline and returns the
// list of floors it missed - the overall recall@1 and every category that carries
// a committed threshold - in a stable order (overall first, then categories in
// RetrievalCategories order). An empty slice means the gate passes. A category the
// baseline sets a floor for but the report never scored is itself a failure, so a
// silently dropped category cannot pass the gate.
func (b Baseline) CheckRetrieval(r RetrievalReport) []RetrievalFailure {
	var failures []RetrievalFailure
	if lt(r.OverallAt1, b.Retrieval.MinOverallRecallAt1) {
		failures = append(failures, RetrievalFailure{Category: "overall", Got: r.OverallAt1, Want: b.Retrieval.MinOverallRecallAt1})
	}
	for _, cat := range RetrievalCategories {
		want, set := b.Retrieval.Categories[cat]
		if !set {
			continue
		}
		got, scored := r.Recall(cat)
		if !scored {
			failures = append(failures, RetrievalFailure{Category: cat, Got: 0, Want: want})
			continue
		}
		if lt(got.RecallAt1, want) {
			failures = append(failures, RetrievalFailure{Category: cat, Got: got.RecallAt1, Want: want})
		}
	}
	return failures
}

// lt reports whether got is below want beyond a small epsilon, so floating-point
// noise in an averaged recall does not fail a gate that is really at its floor.
func lt(got, want float64) bool {
	return got < want-1e-9
}

// FormatFailures renders a retrieval-gate failure list (already in the stable
// order CheckRetrieval emits) as a multi-line message for the eval command and
// test logs, one line per missed floor.
func FormatFailures(failures []RetrievalFailure) string {
	var out string
	for _, f := range failures {
		out += fmt.Sprintf("\n  %-16s recall@1 %.3f below floor %.3f", f.Category, f.Got, f.Want)
	}
	return out
}
