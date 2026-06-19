package service

import (
	"context"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/claimtype"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// The political verify path is the capstone of the French/EU political
// fact-checking redesign (VER-103): behind FACTCHECK_POLITICAL the verify path's
// per-claim verify stage is replaced by classify -> route+retrieve -> two-axis
// verify, and the per-speaker aggregator becomes flag-aware. The path is layered
// onto the existing retrieve-then-verify orchestration (verify_path.go): the
// subtitle/claims/checking/verified event lifecycle, the two-pool concurrency,
// the capacity-shed-to-unchecked semantics, and the curated fast-path borrow are
// all reused unchanged. Only one claim's verify stage differs, behind the optional
// PoliticalConfig collaborators wired here.
//
// Per-claim event/frame contract consumed by VER-104 (frontend) and VER-105
// (eval): a verified political result is a LiveEventResult with ClaimStatus
// verified, Source verified (or curated for a fast borrow), and a *VerifiedVerdict
// whose Literal is accurate/inaccurate/unverifiable, Flags is the subset of the
// closed manipulation vocabulary, Citations are the cited source passages (each a
// domain.SegmentMatch carrying its evidence id and provenance), and Confidence/
// Rationale come from the verifier. The credibility axis (Verdict) is derived from
// Literal so the per-speaker score and the legacy verdict contract keep working.

// PoliticalClassifier tags one atomic claim with its claim type so the political
// path can route it to the right source family. It is the consumer-side port the
// path depends on; the claimtype.Client satisfies it. It never errors: the
// classifier degrades to a safe verifiable default on any model failure, so the
// path always has a route.
type PoliticalClassifier interface {
	Classify(ctx context.Context, claim string) claimtype.Type
}

// PoliticalRetriever routes one classified claim to its authoritative source
// adapter(s) and returns the evidence passages the two-axis verifier reads. It is
// the consumer-side port the political path depends on; the service Router
// satisfies it. Hints carries optional structured selectors an adapter
// understands; the live path supplies none today (nil), so a statistic routes on
// its text alone.
type PoliticalRetriever interface {
	Retrieve(ctx context.Context, claim string, ct claimtype.Type, hints map[string]string) ([]source.Evidence, error)
}

// PoliticalVerdict is the two-axis verifier's judgment for one claim: the literal
// face-value verdict (accurate/inaccurate/unverifiable), the orthogonal
// manipulation flags, the grounding basis, the calibrated confidence, the
// validated citations (already citation-guarded by the adapter), and a French
// rationale.
type PoliticalVerdict struct {
	Literal    string
	Basis      string
	Flags      []string
	Confidence float64
	Citations  []EvidenceCitation
	Rationale  string
}

// PoliticalVerifier judges one atomic claim against retrieved evidence on two
// orthogonal axes (literal verdict plus manipulation flags) and returns a grounded
// two-axis verdict. It is the consumer-side port the political path depends on; the
// verify.Client (via a thin adapter over VerifyPolitical) satisfies it. It errors
// when the judgment is unavailable; the path reports the claim's result with a
// non-fatal error rather than ending the session.
type PoliticalVerifier interface {
	VerifyPolitical(ctx context.Context, claim string, passages []EvidencePassage) (PoliticalVerdict, error)
}

// PoliticalClaimSearcher is the slice of the curated political claim store the
// political fast-path borrows from: approximate nearest-neighbor retrieval over the
// two-axis political_claims corpus by query embedding, nearest-first. It is the
// consumer-side port the political path depends on; *postgres.Store satisfies it via
// SearchPoliticalClaims.
type PoliticalClaimSearcher interface {
	SearchPoliticalClaims(ctx context.Context, query []float32, topK int) ([]domain.PoliticalClaimMatch, error)
}

// PoliticalConfig wires the political verify path's collaborators. Classifier,
// Retriever, and Verifier are required when the path is active; NewVerifyPath
// validates them. CuratedStore is optional: when set, a checkable claim that
// near-matches a curated two-axis political claim borrows its verdict (literal
// verdict + manipulation flags + real source) at or above CuratedTau with no LLM
// call, ahead of routing and verification. When the Political field of
// VerifyPathConfig is nil the path runs the credibility-only verify stage unchanged,
// so FACTCHECK_POLITICAL off is byte-for-byte the legacy behavior.
type PoliticalConfig struct {
	Classifier PoliticalClassifier
	Retriever  PoliticalRetriever
	Verifier   PoliticalVerifier
	// CuratedStore is the curated two-axis claim corpus the fast-path borrows from.
	// Nil disables the borrow, so the path falls through to route+verify.
	CuratedStore PoliticalClaimSearcher
	// CuratedTau is the cosine similarity at or above which a curated near-match is
	// borrowed. A cosine similarity in [-1, 1].
	CuratedTau float64
}

// political reports whether the political path is wired.
func (vp *VerifyPath) political() bool { return vp.pol != nil }

// scorePoliticalClaim is the political replacement for the credibility verify
// stage of scoreClaim: after the curated fast borrow has been ruled out, it
// classifies the claim, routes it to its source adapters, retrieves evidence, and
// runs the two-axis verifier under the same bounded verify pool. It emits the same
// per-claim lifecycle (checking -> verified/unchecked/error) as the credibility
// path, and folds the verdict into the flag-aware aggregator. ret carries the
// curated-matcher embedding already computed for the fast borrow, reused for
// consistency without re-embedding.
func (vp *VerifyPath) scorePoliticalClaim(ctx context.Context, a *LiveAnalyzer, out chan<- LiveEvent, mem *speakerMemory, pu pendingUnit, claim AtomicClaim, ret retrieved) {
	unitID := pu.members[0].id
	seg := pu.members[0].seg

	ct := vp.pol.Classifier.Classify(ctx, claim.Text)

	evidence, err := vp.routeRetrieve(ctx, claim.Text, ct)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "political routing failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		vp.emitClaimError(ctx, out, unitID, claim, seg)
		return
	}

	// Announce checking before the verdict, the same lifecycle position the
	// credibility path uses (it emits checking before verifyClaim, which itself
	// absorbs the no-evidence case): a routed claim always shows checking ->
	// verified/unchecked/error, so a client can key its row transition on the
	// checking frame regardless of whether evidence was found.
	if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: unitID, Segment: seg, ClaimID: claim.ClaimID, ClaimStatus: ClaimStatusChecking}) {
		return
	}

	if len(evidence) == 0 {
		// No routed evidence and no verifier call: the honest "nothing to check
		// against" outcome is unverifiable/knowledge, mirroring the credibility
		// path's no-evidence case.
		verdict := politicalNoEvidenceVerdict()
		vp.cachePut(claim.Text, SourceVerified, verdict, ret.embedding)
		vp.emitVerdict(ctx, out, unitID, claim, seg, SourceVerified, verdict)
		vp.recordSpeakerScore(ctx, out, mem, pu.speaker, verdict)
		vp.recordConsistency(ctx, a, out, mem, pu, claim, ret.embedding)
		return
	}

	verdict, shed, err := vp.verifyPolitical(ctx, claim.Text, evidence)
	if shed {
		_ = sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: unitID, Segment: seg, ClaimID: claim.ClaimID, ClaimStatus: ClaimStatusUnchecked, SkipReason: domain.SkipReasonNotChecked})
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "political verifier failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		vp.emitClaimError(ctx, out, unitID, claim, seg)
		return
	}
	vp.cachePut(claim.Text, SourceVerified, verdict, ret.embedding)
	vp.emitVerdict(ctx, out, unitID, claim, seg, SourceVerified, verdict)
	vp.recordSpeakerScore(ctx, out, mem, pu.speaker, verdict)
	vp.recordConsistency(ctx, a, out, mem, pu, claim, ret.embedding)
}

// routeRetrieve classifies-then-routes one claim under the fast deadline (routing
// is the path's retrieval tier). It returns the routed evidence the verifier reads.
func (vp *VerifyPath) routeRetrieve(ctx context.Context, claim string, ct claimtype.Type) ([]source.Evidence, error) {
	fastCtx, cancel := context.WithTimeout(ctx, vp.fastDeadline)
	defer cancel()
	return vp.pol.Retriever.Retrieve(fastCtx, claim, ct, nil)
}

// verifyPolitical runs the two-axis verifier under the verify pool, returning
// shed=true when the pool and its bounded queue are saturated so the caller emits
// the honest unchecked terminal state. It projects the routed evidence into
// verifier passages and resolves the verifier's citations back to wire matches
// carrying their source provenance.
func (vp *VerifyPath) verifyPolitical(ctx context.Context, claim string, evidence []source.Evidence) (*VerifiedVerdict, bool, error) {
	if !vp.acquireVerifySlot(ctx) {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, nil
	}
	defer func() { <-vp.verifySem }()

	verifyCtx, cancel := context.WithTimeout(ctx, vp.verifyDeadline)
	defer cancel()

	passages := EvidencePassagesFrom(evidence)
	res, err := vp.pol.Verifier.VerifyPolitical(verifyCtx, claim, passages)
	if err != nil {
		return nil, false, err
	}
	return politicalVerdictFromResult(res, evidence), false, nil
}

// politicalVerdictFromResult builds the wire verdict from the two-axis judgment.
// It maps the literal verdict onto the credibility axis (accurate -> credible,
// inaccurate -> disputed, unverifiable -> unverifiable) so the per-speaker score
// and the legacy verdict contract keep working, carries the literal verdict and
// flags through for the two-axis UI, and resolves each validated citation back to
// the routed evidence it grounds (by evidence id) so the cited passage and its
// provenance round-trip. A citation whose id is not among the routed evidence is
// dropped (the adapter's guard already rejected fabricated ids).
func politicalVerdictFromResult(res PoliticalVerdict, evidence []source.Evidence) *VerifiedVerdict {
	byID := make(map[string]source.Evidence, len(evidence))
	for _, e := range evidence {
		byID[e.ID.String()] = e
	}
	citations := make([]domain.SegmentMatch, 0, len(res.Citations))
	for _, c := range res.Citations {
		if e, ok := byID[c.EvidenceID]; ok {
			citations = append(citations, matchFromEvidence(e))
		}
	}
	return &VerifiedVerdict{
		Verdict:    credibilityFromLiteral(res.Literal),
		Basis:      res.Basis,
		Confidence: res.Confidence,
		Citations:  citations,
		Rationale:  res.Rationale,
		Literal:    res.Literal,
		Flags:      res.Flags,
	}
}

// matchFromEvidence projects one routed source passage into the wire match shape
// the UI renders, carrying its stable evidence id and its provenance (publisher
// name and url) as a domain.Source so a citation shows the cited source.
func matchFromEvidence(e source.Evidence) domain.SegmentMatch {
	return domain.SegmentMatch{
		Kind:       domain.MatchKindEvidence,
		Claim:      e.Passage,
		Similarity: 1,
		EvidenceID: e.ID.String(),
		Sources:    []domain.Source{{Title: e.Source.Name, URL: e.Source.URL}},
	}
}

// politicalNoEvidenceVerdict is the honest "nothing to check against" two-axis
// verdict for a claim that routed no evidence: literal unverifiable on a knowledge
// basis, no flags, no citations - the same posture the credibility path takes when
// retrieval is empty.
func politicalNoEvidenceVerdict() *VerifiedVerdict {
	return &VerifiedVerdict{
		Verdict: VerdictUnverifiable,
		Basis:   BasisKnowledge,
		Literal: LiteralUnverifiable,
	}
}

// politicalFastMatch borrows a two-axis verdict from the curated political_claims
// store when its nearest hit clears CuratedTau, reusing the already-computed query
// embedding (no re-embed). It runs ahead of routing and verification, so a recurring
// talking point resolves instantly with its real literal verdict, manipulation
// flags, and source. It is a no-op (ok=false) when the store is not wired, the
// embedding is empty, the search errors (logged, non-fatal), or no hit clears the
// tau; every miss falls through to the route+verify stage, so the curated fast-path
// can never make a claim worse than today.
func (vp *VerifyPath) politicalFastMatch(ctx context.Context, embedding []float32) (*VerifiedVerdict, bool) {
	if vp.pol.CuratedStore == nil || len(embedding) == 0 {
		return nil, false
	}
	matches, err := vp.pol.CuratedStore.SearchPoliticalClaims(ctx, embedding, 1)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.WarnContext(ctx, "political curated fast-path search failed", slog.Any("err", err))
		}
		return nil, false
	}
	if len(matches) == 0 {
		return nil, false
	}
	best := matches[0]
	if similarityFromDistance(best.Distance) < vp.pol.CuratedTau {
		return nil, false
	}
	return politicalCuratedVerdict(best), true
}

// politicalCuratedVerdict builds a borrowed two-axis verdict from a curated
// political_claims near-match: the curated literal verdict and manipulation flags
// carried through verbatim, the credibility axis derived from the literal so the
// per-speaker score and legacy contract keep working, and a single citation pointing
// at the real curated source. Basis is evidence (the curated check is the cited
// source) and Confidence is the near-match similarity, mirroring curatedVerdict. The
// rationale is the curated quoted span (the published figure and its period). Per the
// unverifiable invariant, an unverifiable literal grounds nothing and carries no
// citation.
func politicalCuratedVerdict(m domain.PoliticalClaimMatch) *VerifiedVerdict {
	literal := string(m.LiteralVerdict)
	flags := make([]string, len(m.Flags))
	for i, f := range m.Flags {
		flags[i] = string(f)
	}
	verdict := &VerifiedVerdict{
		Verdict:    credibilityFromLiteral(literal),
		Basis:      BasisEvidence,
		Confidence: similarityFromDistance(m.Distance),
		Rationale:  m.QuotedSpan,
		Literal:    literal,
		Flags:      flags,
	}
	if verdict.Verdict != VerdictUnverifiable {
		verdict.Citations = []domain.SegmentMatch{politicalCuratedMatch(m)}
	}
	return verdict
}

// politicalCuratedMatch projects a curated political near-match into the wire match
// shape the UI renders, carrying the curated claim text and its real source
// provenance (publisher name and url) so the cited source round-trips.
func politicalCuratedMatch(m domain.PoliticalClaimMatch) domain.SegmentMatch {
	return domain.SegmentMatch{
		Kind:       domain.MatchKindClaim,
		Claim:      m.Text,
		Similarity: similarityFromDistance(m.Distance),
		EvidenceID: m.ID,
		Sources:    []domain.Source{{Title: m.SourceName, URL: m.SourceURL}},
	}
}

// similarityFromDistance converts a pgvector cosine distance in [0, 2] to cosine
// similarity in [-1, 1]: identical vectors (distance 0) map to similarity 1.
func similarityFromDistance(distance float32) float64 {
	return 1 - float64(distance)
}

// literalFromCredibility maps a curated borrow's credibility verdict onto the
// literal axis so a borrowed verdict on the political path also carries its
// face-value verdict (credible -> accurate, disputed -> inaccurate, unverifiable
// -> unverifiable). It is the inverse of credibilityFromLiteral for the values the
// curated corpus produces.
func literalFromCredibility(verdict string) string {
	switch verdict {
	case VerdictCredible:
		return LiteralAccurate
	case VerdictDisputed:
		return LiteralInaccurate
	default:
		return LiteralUnverifiable
	}
}

// credibilityFromLiteral maps the literal axis onto the credibility vocabulary the
// per-speaker aggregator scores in: an accurate claim is credible (moves the score
// up), an inaccurate one disputed (moves it down), and an unverifiable one stays
// unverifiable (tally only, excluded from the score).
func credibilityFromLiteral(literal string) string {
	switch literal {
	case LiteralAccurate:
		return VerdictCredible
	case LiteralInaccurate:
		return VerdictDisputed
	default:
		return VerdictUnverifiable
	}
}
