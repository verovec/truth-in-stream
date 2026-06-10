package domain

// SkipReason explains why a transcript segment was not fact-checked. A skip is
// categorically distinct from a Verdict: a Verdict (corroborates, contradicts,
// unclear) is the outcome of checking a claim against the corpus, whereas a
// SkipReason records that the system declined to check at all. The two never
// share a value space, so a skipped segment can never be mistaken for a
// verdict anywhere it is stored or shown.
type SkipReason string

const (
	// SkipReasonNone is the zero value: the segment was checked, so it carries
	// a (possibly empty) set of matches rather than a skip reason.
	SkipReasonNone SkipReason = ""
	// SkipReasonNotAClaim means the segment is not a checkable, declarative
	// factual assertion (a question, opinion, greeting, prediction, or
	// fragment).
	SkipReasonNotAClaim SkipReason = "not_a_claim"
	// SkipReasonNotCovered means the reference corpus does not plausibly cover
	// the claim, so no evidence-grounded verdict is possible.
	SkipReasonNotCovered SkipReason = "not_covered"
)

// Valid reports whether r is one of the known skip reasons. The empty reason
// (a checked segment) is valid: a segment is either checked or skipped for a
// known cause, never skipped for an unknown one.
func (r SkipReason) Valid() bool {
	switch r {
	case SkipReasonNone, SkipReasonNotAClaim, SkipReasonNotCovered:
		return true
	default:
		return false
	}
}

// PrecheckDecision is the outcome of the check-worthiness gate for one
// segment. A checkable segment proceeds to matching; a skipped one carries the
// reason it was declined and never reaches the matcher. Modeling the decision
// explicitly keeps the skip-vs-check branch in one place and the reason in the
// type system rather than in an out-of-band boolean.
type PrecheckDecision struct {
	Checkable bool
	Reason    SkipReason
}

// Checkable reports a segment worth matching against the corpus.
func Checkable() PrecheckDecision {
	return PrecheckDecision{Checkable: true, Reason: SkipReasonNone}
}

// Skipped reports a segment the gate declined to check, for the given reason.
func Skipped(reason SkipReason) PrecheckDecision {
	return PrecheckDecision{Checkable: false, Reason: reason}
}
