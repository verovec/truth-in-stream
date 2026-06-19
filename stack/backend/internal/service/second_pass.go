package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ClaimReverifier re-judges one atomic claim against the same retrieved evidence
// on a deeper reasoning model, returning a grounded verdict with cited spans. It
// is the consumer-side port the second pass depends on; the verify.Client's
// Reverify (via a thin adapter) satisfies it. Like ClaimVerifier it errors when
// the deeper judgment is unavailable (transport, decode); the second pass logs and
// keeps the fast verdict rather than failing the claim.
type ClaimReverifier interface {
	Reverify(ctx context.Context, claim string, passages []EvidencePassage) (ClaimVerdict, error)
}

// SecondPassConfig configures the deeper-reasoner second pass. It is nil unless
// the feature is wired (flag on, reasoner keyed): a nil config is a hard no-op, so
// the default-off path is byte-for-byte the legacy verify path. Reverifier is the
// deeper reasoning model. MidBandLo and MidBandHi bound the fast-verdict confidence
// band that qualifies for a second look (inclusive); a verdict outside the band is
// left untouched. Deadline bounds one reverify call so a slow or expensive reasoner
// can never stall the claim's worker indefinitely.
type SecondPassConfig struct {
	Reverifier ClaimReverifier
	MidBandLo  float64
	MidBandHi  float64
	Deadline   time.Duration
}

// secondPass is the validated, wired second-pass policy. It is nil on the
// VerifyPath when the feature is off, and every entry point guards on nil, so the
// disabled path adds no work and no latency.
type secondPass struct {
	reverifier ClaimReverifier
	midBandLo  float64
	midBandHi  float64
	deadline   time.Duration
}

// newSecondPass validates and builds the policy from its config. It fails when a
// required collaborator is missing or a bound is out of range (a band outside
// [0,1] or inverted, a non-positive deadline), so a misconfiguration surfaces at
// wiring time rather than as a silently skipped second pass.
func newSecondPass(cfg SecondPassConfig) (*secondPass, error) {
	switch {
	case cfg.Reverifier == nil:
		return nil, errors.New("service: second pass requires a reverifier")
	case cfg.MidBandLo < 0 || cfg.MidBandLo > 1:
		return nil, fmt.Errorf("service: second pass mid-band low %v outside [0, 1]", cfg.MidBandLo)
	case cfg.MidBandHi < 0 || cfg.MidBandHi > 1:
		return nil, fmt.Errorf("service: second pass mid-band high %v outside [0, 1]", cfg.MidBandHi)
	case cfg.MidBandLo > cfg.MidBandHi:
		return nil, fmt.Errorf("service: second pass mid-band low %v above high %v", cfg.MidBandLo, cfg.MidBandHi)
	case cfg.Deadline <= 0:
		return nil, fmt.Errorf("service: second pass deadline must be positive, got %s", cfg.Deadline)
	}
	return &secondPass{
		reverifier: cfg.Reverifier,
		midBandLo:  cfg.MidBandLo,
		midBandHi:  cfg.MidBandHi,
		deadline:   cfg.Deadline,
	}, nil
}

// qualifies is the gating predicate, the heart of the card's "deeper look only
// where it can help" rule. A fast verdict qualifies for a second pass only when it
// is grounded in surviving evidence (basis evidence), it has retrieved passages to
// re-read, and its confidence falls inside the configurable mid band. It NEVER
// qualifies a knowledge-basis or unverifiable verdict: a deeper model cannot
// manufacture evidence retrieval never found, and escalating an ungrounded verdict
// would only tempt it to assert unsourced confidence past the cap. It is a pure
// function so the gate is table-testable without the model.
func (sp *secondPass) qualifies(verdict *VerifiedVerdict, passageCount int) bool {
	if verdict == nil || passageCount == 0 {
		return false
	}
	if verdict.Basis != BasisEvidence {
		return false
	}
	return verdict.Confidence >= sp.midBandLo && verdict.Confidence <= sp.midBandHi
}

// upgrade folds a reasoning re-judgment into the fast verdict. It is deterministic
// and table-testable, and it enforces the second pass's two invariants:
//
//   - The knowledge-confidence cap can never be exceeded by an upgrade. A reasoning
//     verdict that came back without a surviving citation (basis knowledge) is the
//     deeper model's own ungrounded judgment; it can refine the verdict but its
//     confidence is bounded at the cap, exactly as the fast path bounds a
//     knowledge-only verdict. A grounded (evidence) re-judgment keeps its clamped
//     confidence.
//   - Only a reasoning verdict that is itself grounded (evidence basis with at least
//     one surviving citation) is allowed to REPLACE the fast verdict's state and
//     confidence. If the reasoner came back ungrounded, the original grounded fast
//     verdict stands - the deeper look may not downgrade a grounded verdict to an
//     ungrounded one, only strengthen a grounded one.
//
// The reverifier's result is already citation-guarded (ValidateCitations ran in the
// adapter), so basis knowledge here means "no citation survived"; this function is
// the service-layer enforcement on top of that, defense in depth for the cap.
func (sp *secondPass) upgrade(orig *VerifiedVerdict, reasoned ClaimVerdict, matches []domain.SegmentMatch) *VerifiedVerdict {
	if reasoned.Basis != BasisEvidence || len(reasoned.Citations) == 0 {
		// The deeper model could not ground its re-judgment; keep the grounded fast
		// verdict rather than trading it for an ungrounded one.
		return orig
	}
	upgraded := verdictFromResult(reasoned, matches)
	// Defense in depth: a grounded re-judgment keeps its clamped confidence, but if
	// resolving citations against the retrieved matches dropped them all, the verdict
	// is no longer grounded and must not exceed the knowledge cap.
	if len(upgraded.Citations) == 0 {
		upgraded.Basis = BasisKnowledge
		upgraded.Confidence = capKnowledgeConfidence(upgraded.Confidence)
	}
	return upgraded
}

// capKnowledgeConfidence mirrors the verify package's knowledge cap at the service
// layer: a verdict not grounded in a surviving citation can never render
// high-confidence, no matter which pass produced it.
func capKnowledgeConfidence(c float64) float64 {
	const knowledgeConfidenceCap = 0.6
	if c < 0 {
		return 0
	}
	return min(c, knowledgeConfidenceCap)
}

// reverify runs the deeper reasoning call under its own bounded deadline,
// returning the citation-guarded re-judgment. It is bounded independently of the
// verify pool: the second pass never acquires a verify-pool slot, so it cannot
// consume the live path's best-effort scoring capacity.
func (sp *secondPass) reverify(ctx context.Context, claim string, passages []EvidencePassage) (ClaimVerdict, error) {
	reverifyCtx, cancel := context.WithTimeout(ctx, sp.deadline)
	defer cancel()
	return sp.reverifier.Reverify(reverifyCtx, claim, passages)
}

// maybeReverify is the second pass's single entry point on the verify path. It is
// a no-op when the feature is off (sp nil), when the fast verdict does not qualify,
// or when the reasoning call fails - in every case the fast verdict already emitted
// to the viewer stands untouched. When the deeper model upgrades a qualifying
// verdict, it re-emits the verified frame for the same claim id so the client
// replaces the displayed verdict in place. It runs AFTER the fast verdict is
// emitted, so it never delays the live per-claim result, and outside the verify
// pool, so it never consumes a scoring slot.
//
// It deliberately does NOT re-fold the upgraded verdict into the speaker score: the
// fast verdict was already observed in that running aggregate, and re-observing the
// same claim would double-count it. The second pass corrects the displayed
// per-claim verdict, not the speaker tally - so a one-claim upgrade can never skew
// the aggregate by counting the claim twice.
func (vp *VerifyPath) maybeReverify(ctx context.Context, out chan<- LiveEvent, pu pendingUnit, claim AtomicClaim, fast *VerifiedVerdict, ret retrieved) {
	sp := vp.secondPass
	// qualifies counts the evidence-bearing passages the reverifier would actually
	// receive (those carrying an evidence id), not every retrieved match, so the gate
	// reflects what the deeper model can ground against rather than the raw hit count.
	passages := passagesFromMatches(ret.matches)
	if sp == nil || !sp.qualifies(fast, len(passages)) {
		return
	}
	reasoned, err := sp.reverify(ctx, claim.Text, passages)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "second-pass reverify failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		return
	}
	upgraded := sp.upgrade(fast, reasoned, ret.matches)
	if upgraded == fast {
		return
	}
	// Overwrite the cache entry so a repeat of the same claim within the TTL replays
	// the upgraded verdict, not the stale fast one; the retrieval embedding is carried
	// through so a cache-hit replay still feeds consistency detection.
	vp.cachePut(claim.Text, SourceVerified, upgraded, ret.embedding)
	vp.emitVerdict(ctx, out, pu.members[0].id, claim, pu.members[0].seg, SourceVerified, upgraded)
}
