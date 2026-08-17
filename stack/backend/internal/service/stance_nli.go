package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// StanceResult is one passage's calibrated stance toward a claim: the
// probability that the passage entails (supports), is neutral toward, or
// contradicts (refutes) it. The three components sum to one.
type StanceResult struct {
	Entailment    float64
	Neutral       float64
	Contradiction float64
}

// EvidenceStanceScorer scores a claim against evidence passages with a local
// NLI cross-encoder, one stance per passage in order. An error means the
// scores are unavailable (model missing, inference timeout, overload), never
// a judgment about the claim.
type EvidenceStanceScorer interface {
	ScoreStances(ctx context.Context, claim string, passages []string) ([]StanceResult, error)
}

// StanceConfig wires the local NLI stance stage in front of the generative
// verifier. Thresholds come from the training pipeline's calibration run: a
// verdict is decided locally only when at least MinAgree passages cross their
// stance threshold and no passage crosses the opposing one; anything mixed,
// neutral, or under-threshold escalates to the verifier unchanged.
type StanceConfig struct {
	Scorer              EvidenceStanceScorer
	EntailThreshold     float64
	ContradictThreshold float64
	MinAgree            int
	// MaxPassages caps how many retrieved passages are cross-encoded per
	// claim, bounding the stage's fixed CPU cost. Retrieval order is kept, so
	// the cap keeps the highest-ranked evidence.
	MaxPassages int
}

// stanceStage is the validated runtime form of StanceConfig.
type stanceStage struct {
	scorer              EvidenceStanceScorer
	entailThreshold     float64
	contradictThreshold float64
	minAgree            int
	maxPassages         int
}

// newStanceStage validates the configuration once at construction.
func newStanceStage(cfg *StanceConfig) (*stanceStage, error) {
	if cfg == nil {
		return nil, nil
	}
	if cfg.Scorer == nil {
		return nil, fmt.Errorf("verify path: stance stage requires a scorer")
	}
	if !(cfg.EntailThreshold > 0 && cfg.EntailThreshold <= 1) || !(cfg.ContradictThreshold > 0 && cfg.ContradictThreshold <= 1) {
		return nil, fmt.Errorf("verify path: stance thresholds must be probabilities in (0, 1], got entail %v contradict %v", cfg.EntailThreshold, cfg.ContradictThreshold)
	}
	if cfg.MinAgree < 1 {
		return nil, fmt.Errorf("verify path: stance min agree must be at least 1, got %d", cfg.MinAgree)
	}
	if cfg.MaxPassages < 1 {
		return nil, fmt.Errorf("verify path: stance max passages must be at least 1, got %d", cfg.MaxPassages)
	}
	return &stanceStage{
		scorer:              cfg.Scorer,
		entailThreshold:     cfg.EntailThreshold,
		contradictThreshold: cfg.ContradictThreshold,
		minAgree:            cfg.MinAgree,
		maxPassages:         cfg.MaxPassages,
	}, nil
}

// Viewer-facing rationales for locally-decided verdicts. Rationales are
// always French, whatever the configured locale, matching the verifier's
// contract.
const (
	stanceSupportRationale = "Les sources citées confirment directement cette affirmation."
	stanceRefuteRationale  = "Les sources citées contredisent directement cette affirmation."
)

// stanceResolve tries to decide the claim's verdict locally from the
// retrieved evidence. It returns nil whenever the stage should escalate to
// the generative verifier: stage off, no citable passages, scorer failure
// (fail-open), mixed or under-threshold stances. A non-nil verdict always
// carries basis evidence, at least one citation resolved from the retrieved
// matches, and a confidence equal to the mean calibrated probability of the
// agreeing passages.
func (vp *VerifyPath) stanceResolve(ctx context.Context, claim string, matches []domain.SegmentMatch) *VerifiedVerdict {
	st := vp.nliStance
	if st == nil {
		return nil
	}
	citable := make([]domain.SegmentMatch, 0, len(matches))
	texts := make([]string, 0, len(matches))
	for _, m := range matches {
		if m.EvidenceID == "" {
			continue
		}
		citable = append(citable, m)
		texts = append(texts, m.Claim)
		if len(citable) == st.maxPassages {
			break
		}
	}
	if len(citable) == 0 {
		return nil
	}

	stances, err := st.scorer.ScoreStances(ctx, claim, texts)
	if err != nil {
		vp.logger.WarnContext(ctx, "nli stance scorer failed; escalating to the verifier", slog.String("error", err.Error()))
		return nil
	}
	if len(stances) != len(citable) {
		vp.logger.WarnContext(ctx, "nli stance scorer returned a mismatched count; escalating to the verifier",
			slog.Int("passages", len(citable)), slog.Int("stances", len(stances)))
		return nil
	}

	var entailing, contradicting []int
	for i, s := range stances {
		if s.Entailment >= st.entailThreshold {
			entailing = append(entailing, i)
		}
		if s.Contradiction >= st.contradictThreshold {
			contradicting = append(contradicting, i)
		}
	}

	switch {
	case len(entailing) >= st.minAgree && len(contradicting) == 0:
		return stanceVerdict(VerdictCredible, stanceSupportRationale, citable, stances, entailing, func(s StanceResult) float64 { return s.Entailment })
	case len(contradicting) >= st.minAgree && len(entailing) == 0:
		return stanceVerdict(VerdictDisputed, stanceRefuteRationale, citable, stances, contradicting, func(s StanceResult) float64 { return s.Contradiction })
	default:
		return nil
	}
}

// stanceVerdict assembles the locally-decided verdict: the agreeing passages
// become the citations and their mean calibrated probability the confidence.
func stanceVerdict(verdict, rationale string, matches []domain.SegmentMatch, stances []StanceResult, agreeing []int, prob func(StanceResult) float64) *VerifiedVerdict {
	citations := make([]domain.SegmentMatch, 0, len(agreeing))
	total := 0.0
	for _, i := range agreeing {
		citations = append(citations, matches[i])
		total += prob(stances[i])
	}
	return &VerifiedVerdict{
		Verdict:        verdict,
		Basis:          BasisEvidence,
		Confidence:     total / float64(len(agreeing)),
		Citations:      citations,
		Rationale:      rationale,
		DecidedLocally: true,
	}
}
