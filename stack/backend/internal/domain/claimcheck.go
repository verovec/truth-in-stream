package domain

import "time"

// ClaimCheck decision paths: which stage settled the claim. The vocabulary is
// enforced here, not by a database CHECK, matching the evidence-chunk kind
// convention, so a future stage (a local classifier band, an NLI consensus) is
// a new constant, never a migration.
const (
	// DecisionGateSkip is a unit the check-worthiness gate rejected; no claim
	// was extracted and no retrieval or verdict ran.
	DecisionGateSkip = "gate-skip"
	// DecisionNoClaim is a unit the decomposer emptied: it reached the claim
	// extractor but carried no verifiable claim.
	DecisionNoClaim = "no-claim"
	// DecisionCache is a verdict replayed from the semantic claim cache.
	DecisionCache = "cache"
	// DecisionPoliticalCurated is a two-axis verdict borrowed from the curated
	// political corpus with no generative call.
	DecisionPoliticalCurated = "political-curated"
	// DecisionCurated is a credibility verdict borrowed from the curated claims
	// corpus with no generative call.
	DecisionCurated = "curated"
	// DecisionNoEvidence is the honest unverifiable outcome when retrieval (or
	// political routing) produced nothing to judge against; no generative call.
	DecisionNoEvidence = "no-evidence"
	// DecisionVerified is a verdict produced by the generative verifier.
	DecisionVerified = "verified"
	// DecisionLocalNLI is a verdict the local NLI stance stage decided from
	// the retrieved evidence alone, no generative call.
	DecisionLocalNLI = "local-nli"
	// DecisionShed is a claim dropped by verify-pool saturation, terminal
	// unchecked.
	DecisionShed = "shed"
	// DecisionError is a retrieval or verifier failure, terminal error.
	DecisionError = "error"
)

// ClaimCheck is one telemetry row of the fact-check pipeline: what each stage
// decided for one claim occurrence (or one gate-skipped unit) and what it cost.
// It is an append-only analytical record - never read by the viewer-facing
// paths - feeding threshold calibration, the generative-call budget, and
// training-set builds for the local classifiers. Every field a query filters or
// aggregates on is typed, per the schema's standing rule; the row deliberately
// carries no jsonb.
type ClaimCheck struct {
	OccurredAt time.Time
	// SessionKind labels the pipeline entry: "live" for the streaming loop
	// (live video, TV, pre-analysis all share it), "batch" for text analysis.
	SessionKind string
	Locale      string
	Speaker     string
	// UnitText is the sentence group the claim came from; ClaimText is the
	// decomposed atomic claim, empty for unit-level rows (gate skips).
	UnitText  string
	ClaimText string
	// DecisionPath is which stage settled the row (Decision* constants);
	// SkipReason carries the gate/decomposer skip vocabulary for skip rows.
	DecisionPath string
	SkipReason   string
	// Retrieval quality at decision time: the strongest similarity in the fused
	// result, the candidate count, and the per-corpus split.
	RetrievalTop          float64
	RetrievalCandidates   int
	RetrievalClaimHits    int
	RetrievalEvidenceHits int
	// Verdict axes as emitted (empty on skip/shed/error rows).
	Verdict    string
	Basis      string
	Literal    string
	Confidence float64
	Source     string
	// Escalated marks a verdict the terminal reasoning gate re-judged and
	// replaced; LLMCalls counts the generative calls this row's decision spent
	// (verifier, political classifier+verifier, reasoning gate; the per-unit
	// decomposition call is amortized across the unit's claims and not counted).
	Escalated bool
	LLMCalls  int
	LatencyMS int64
}
