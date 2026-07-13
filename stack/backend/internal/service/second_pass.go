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

// SecondPassConfig configures the terminal reasoning gate (VER-192). It is nil
// unless the feature is wired (flag on, reasoner keyed): a nil config is a hard
// no-op, so the default-off path is byte-for-byte the legacy verify path. Reverifier
// is the deeper reasoning model. TriggerBelow is the confidence floor below which
// the pipeline's best verdict is weak enough to escalate (a verdict at or above the
// floor is already strong and never escalated); an unverifiable verdict always
// escalates. MinConfidence is the grounded confidence the reasoner's re-judgment
// must reach before it is allowed to REPLACE the weak verdict. Deadline bounds one
// reverify call so a slow or expensive reasoner can never stall the claim's worker
// indefinitely.
type SecondPassConfig struct {
	Reverifier    ClaimReverifier
	TriggerBelow  float64
	MinConfidence float64
	Deadline      time.Duration
}

// secondPass is the validated, wired terminal-gate policy. It is nil on the
// VerifyPath when the feature is off, and every entry point guards on nil, so the
// disabled path adds no work and no latency.
type secondPass struct {
	reverifier    ClaimReverifier
	triggerBelow  float64
	minConfidence float64
	deadline      time.Duration
}

// newSecondPass validates and builds the policy from its config. It fails when a
// required collaborator is missing or a threshold is out of range (outside [0,1] or
// a non-positive deadline), so a misconfiguration surfaces at wiring time rather
// than as a silently skipped gate.
func newSecondPass(cfg SecondPassConfig) (*secondPass, error) {
	switch {
	case cfg.Reverifier == nil:
		return nil, errors.New("service: terminal gate requires a reverifier")
	case cfg.TriggerBelow < 0 || cfg.TriggerBelow > 1:
		return nil, fmt.Errorf("service: terminal gate trigger floor %v outside [0, 1]", cfg.TriggerBelow)
	case cfg.MinConfidence < 0 || cfg.MinConfidence > 1:
		return nil, fmt.Errorf("service: terminal gate min confidence %v outside [0, 1]", cfg.MinConfidence)
	case cfg.Deadline <= 0:
		return nil, fmt.Errorf("service: terminal gate deadline must be positive, got %s", cfg.Deadline)
	}
	return &secondPass{
		reverifier:    cfg.Reverifier,
		triggerBelow:  cfg.TriggerBelow,
		minConfidence: cfg.MinConfidence,
		deadline:      cfg.Deadline,
	}, nil
}

// weak is the gating predicate, the heart of the card's terminal-gate rule: run the
// deeper reasoner only when the pipeline's best verdict is weak - it is unverifiable,
// or its confidence sits below the trigger floor. A verdict at or above the floor is
// already strong and is never escalated. It requires passageCount > 0 because the
// reasoner grounds its re-judgment in retrieved evidence: with nothing to re-read it
// can do no better than the honest weak verdict the pipeline already produced, so a
// no-evidence unverifiable stands. It is a pure function so the gate is table-testable
// without the model. This inverts the old mid-band, evidence-only qualifier: the gate
// now fires precisely where the pipeline was least sure, regardless of basis.
func (sp *secondPass) weak(verdict *VerifiedVerdict, passageCount int) bool {
	if verdict == nil || passageCount == 0 {
		return false
	}
	return verdict.Verdict == VerdictUnverifiable || verdict.Confidence < sp.triggerBelow
}

// accept reports whether a reasoning re-judgment is strong enough to REPLACE the
// pipeline's weak verdict: it must be grounded (evidence basis with at least one
// surviving citation) AND reach the terminal-gate confidence floor. Anything less -
// an ungrounded re-judgment, or a grounded-but-tentative one - is not adopted: the
// gate prefers the pipeline's honest weak verdict over a shaky upgrade, and never
// asserts a confident verdict the deeper model could not itself ground. It is a pure
// function so the acceptance rule is table-testable without the model.
func (sp *secondPass) accept(reasoned ClaimVerdict) bool {
	return reasoned.Basis == BasisEvidence &&
		len(reasoned.Citations) > 0 &&
		reasoned.Confidence >= sp.minConfidence
}

// upgrade folds a reasoning re-judgment into the pipeline's weak credibility verdict
// under the terminal-gate acceptance rule. It is deterministic and table-testable.
// When the re-judgment is accepted (grounded AND at least MinConfidence) it replaces
// the weak verdict; otherwise the prior verdict stands - an unverifiable prior stays
// unverifiable and a low-confidence-but-valid prior is retained rather than traded
// for an ungrounded or tentative upgrade. This makes the precedence explicit: the
// gate can only strengthen a weak verdict into a grounded, high-confidence one, never
// weaken it or replace it with an unsourced guess.
func (sp *secondPass) upgrade(orig *VerifiedVerdict, reasoned ClaimVerdict, matches []domain.SegmentMatch) *VerifiedVerdict {
	if !sp.accept(reasoned) {
		return orig
	}
	upgraded := verdictFromResult(reasoned, matches)
	// A citation-guarded evidence re-judgment whose ids fail to resolve against the
	// retrieved matches is no longer grounded; do not adopt it - keep the prior verdict
	// rather than emit an ungrounded high-confidence upgrade.
	if len(upgraded.Citations) == 0 {
		return orig
	}
	return upgraded
}

// gateReverify runs the terminal gate's shared pre-fold steps for all four entry
// points (live/batch x credibility/political): given the passages the reasoner will
// ground against, it returns the deeper re-judgment when the verdict is weak and the
// reasoning call succeeds, or ok=false (a no-op) when the verdict is already strong or
// the call fails (logged, non-fatal). The caller has already guarded vp.secondPass !=
// nil (so the default-off path allocates no passages) and folds the result with its own
// upgrade rule - sp.upgrade on the credibility axis, sp.upgradePolitical on the two-axis
// path - so the one place that changes when the gate protocol changes is here, not four.
// stage names the calling path (e.g. "live credibility", "batch political") so a
// reverify failure in the logs still says which pipeline and axis degraded.
func (vp *VerifyPath) gateReverify(ctx context.Context, stage string, claim AtomicClaim, fast *VerifiedVerdict, passages []EvidencePassage) (ClaimVerdict, bool) {
	sp := vp.secondPass
	if !sp.weak(fast, len(passages)) {
		return ClaimVerdict{}, false
	}
	reasoned, err := sp.reverify(ctx, claim.Text, passages)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "terminal-gate reverify failed", slog.String("stage", stage), slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		return ClaimVerdict{}, false
	}
	return reasoned, true
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

// maybeReverify is the terminal gate's single entry point on the credibility verify
// path. It is a no-op when the feature is off (sp nil), when the fast verdict is not
// weak enough to escalate, or when the reasoning call fails - in every case the fast
// verdict already emitted to the viewer stands untouched. When the deeper model
// grounds a high-confidence re-judgment of a weak verdict, it re-emits the verified
// frame for the same claim id so the client replaces the displayed verdict in place.
// It runs AFTER the fast verdict is emitted, so it never delays the live per-claim
// result, and outside the verify pool, so it never consumes a scoring slot.
//
// When it upgrades a weak verdict it also corrects the speaker tally, moving the claim
// from its prior bucket to the new one (recordSpeakerReTally): the fast verdict was
// already counted, so this MOVES that single count rather than adding a second, and the
// aggregate credibility breakdown stays consistent with the upgraded per-claim verdict
// the viewer now sees. This is required precisely because the gate now fires on weak
// (including unverifiable) verdicts: an unverifiable-to-definite upgrade would otherwise
// leave the tally showing the claim as unverifiable while the UI shows it disputed.
func (vp *VerifyPath) maybeReverify(ctx context.Context, out chan<- LiveEvent, mem *speakerMemory, pu pendingUnit, claim AtomicClaim, fast *VerifiedVerdict, ret retrieved) {
	if vp.secondPass == nil {
		return
	}
	// passagesFromMatches counts the evidence-bearing passages the reverifier would
	// actually receive (those carrying an evidence id), not every retrieved match, so
	// the gate reflects what the deeper model can ground against rather than the raw hit
	// count.
	reasoned, ok := vp.gateReverify(ctx, "live credibility", claim, fast, passagesFromMatches(ret.matches))
	if !ok {
		return
	}
	upgraded := vp.secondPass.upgrade(fast, reasoned, ret.matches)
	if upgraded == fast {
		return
	}
	// Overwrite the cache entry so a repeat of the same claim within the TTL replays
	// the upgraded verdict, not the stale fast one; the retrieval embedding is carried
	// through so a cache-hit replay still feeds consistency detection.
	vp.cachePut(ret.embedding, claim.Text, SourceVerified, upgraded)
	vp.emitVerdict(ctx, out, pu.members[0].id, claim, pu.members[0].seg, SourceVerified, upgraded)
	vp.recordSpeakerReTally(ctx, out, mem, pu.speaker, fast, upgraded)
}

// applyReverifyBatch is the batch counterpart of maybeReverify: it re-judges a weak
// fast verdict with the deeper reasoner and returns the upgraded verdict, or the
// original fast verdict when the feature is off, the verdict is not weak enough to
// escalate, or the reasoning call fails. It emits nothing. Batch document analysis
// has no realtime constraint, so it applies the same terminal-gate upgrade the live
// path does, keeping a document's verdict for a sentence identical to what the live
// path would show for the same sentence.
func (vp *VerifyPath) applyReverifyBatch(ctx context.Context, claim AtomicClaim, fast *VerifiedVerdict, ret retrieved) *VerifiedVerdict {
	if vp.secondPass == nil {
		return fast
	}
	reasoned, ok := vp.gateReverify(ctx, "batch credibility", claim, fast, passagesFromMatches(ret.matches))
	if !ok {
		return fast
	}
	return vp.secondPass.upgrade(fast, reasoned, ret.matches)
}
