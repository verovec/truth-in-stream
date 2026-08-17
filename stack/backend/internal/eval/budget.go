package eval

import "fmt"

// BudgetBaseline is the committed generative-cost ceiling (VER-230): the
// predicted number of generative calls per checked claim on the default
// vector-first configuration, composed offline from the committed fixtures so
// a cost regression fails CI exactly like an accuracy regression. It is an
// additive key in baseline.json.
//
// The prediction covers the per-claim stages the local models absorb: the
// check-worthiness gate (fixture in-band share), the verdict stage (share of
// stance cases the NLI consensus escalates), and the second pass (share of
// locally-decided verdicts whose confidence sits under the trigger). Claim
// decomposition stays a constant one call per spoken unit by design and is
// out of this ratio; the live telemetry rows (claim_checks.llm_calls) remain
// the ground truth the prediction is calibrated against.
type BudgetBaseline struct {
	// MaxLLMCallsPerClaim ceilings the summed per-claim prediction.
	MaxLLMCallsPerClaim float64 `json:"max_llm_calls_per_claim"`
	// SecondPassTriggerBelow mirrors the shipped FACTCHECK_SECOND_PASS
	// trigger so the prediction and production agree on what "weak" means; a
	// drift here is a reviewed baseline change, not a silent one.
	SecondPassTriggerBelow float64 `json:"second_pass_trigger_below"`
}

// BudgetReport is the per-stage predicted generative-call rate.
type BudgetReport struct {
	GateRate       float64
	VerdictRate    float64
	SecondPassRate float64
	Total          float64
}

// RunBudget composes the predicted generative calls per claim from the two
// committed fixtures at their recorded operating points.
func RunBudget(gate GateGolden, stance NLIGolden, secondPassTriggerBelow float64) BudgetReport {
	rep := BudgetReport{}
	inBand := 0
	for _, c := range gate.Cases {
		if c.Score >= gate.BandLow && c.Score < gate.BandHigh {
			inBand++
		}
	}
	rep.GateRate = float64(inBand) / float64(len(gate.Cases))

	decided, weak := 0, 0
	for _, c := range stance.Cases {
		decision := nliDecide(c.Probs, stance.EntailThreshold, stance.ContradictThreshold, stance.MinAgree)
		if decision == "escalate" {
			continue
		}
		decided++
		if stanceConfidence(c.Probs, decision, stance.EntailThreshold, stance.ContradictThreshold) < secondPassTriggerBelow {
			weak++
		}
	}
	total := len(stance.Cases)
	rep.VerdictRate = float64(total-decided) / float64(total)
	rep.SecondPassRate = float64(weak) / float64(total)
	rep.Total = rep.GateRate + rep.VerdictRate + rep.SecondPassRate
	return rep
}

// stanceConfidence mirrors the service stage's confidence: the mean
// calibrated probability of the agreeing passages for the decided stance.
func stanceConfidence(probs [][]float64, decision string, entailThreshold, contradictThreshold float64) float64 {
	idx, threshold := 0, entailThreshold
	if decision == "refute" {
		idx, threshold = 2, contradictThreshold
	}
	sum, n := 0.0, 0
	for _, row := range probs {
		if row[idx] >= threshold {
			sum += row[idx]
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// Format renders the budget report for the eval command output.
func (r BudgetReport) Format() string {
	return fmt.Sprintf(
		"generative-call budget (predicted per checked claim):\n"+
			"  gate stage                %.3f\n"+
			"  verdict stage             %.3f\n"+
			"  second pass               %.3f\n"+
			"  total                     %.3f",
		r.GateRate, r.VerdictRate, r.SecondPassRate, r.Total)
}

// CheckBudget compares the predicted total against the committed ceiling. A
// nil receiver section means the baseline does not gate cost yet.
func (b Baseline) CheckBudget(r BudgetReport) []GateFailure {
	if b.Budget == nil {
		return nil
	}
	var failures []GateFailure
	if lt(b.Budget.MaxLLMCallsPerClaim, r.Total) {
		failures = append(failures, GateFailure{Metric: "llm-calls-per-claim", Got: r.Total, Want: b.Budget.MaxLLMCallsPerClaim, Ceiling: true})
	}
	return failures
}
