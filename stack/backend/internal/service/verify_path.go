package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ClaimStatus is the lifecycle state of one atomic claim's verdict on the
// retrieve-then-verify path. A claim starts pending (announced in
// LiveEventClaims), may transit checking (a verify call is in flight), and ends
// either verified (a verdict was emitted), unchecked (the verify pool was
// saturated, an honest terminal state rather than a silent drop), or error (a
// retrieval or verifier failure, distinct from a reached verdict).
type ClaimStatus string

const (
	// ClaimStatusPending announces an atomic claim with no verdict yet.
	ClaimStatusPending ClaimStatus = "pending"
	// ClaimStatusChecking marks a claim whose verify call is in flight, the
	// placeholder the client renders before the verified verdict replaces it.
	ClaimStatusChecking ClaimStatus = "checking"
	// ClaimStatusVerified marks a claim whose verdict has been emitted, whether
	// borrowed from a curated near-match (Source curated) or judged by the
	// verifier reading evidence (Source verified).
	ClaimStatusVerified ClaimStatus = "verified"
	// ClaimStatusUnchecked marks a claim shed because the verify pool was
	// saturated; its SkipReason is the capacity shed reason. It is terminal: the
	// claim is honestly reported as not checked rather than dropped.
	ClaimStatusUnchecked ClaimStatus = "unchecked"
	// ClaimStatusError marks a claim whose retrieval or verify call failed; its Err
	// carries the reason. It is terminal and distinct from a reached verdict: the
	// client can tell a failed claim apart from a verified one or a capacity shed.
	ClaimStatusError ClaimStatus = "error"
)

// VerdictSource tags where a verified claim's verdict came from: a borrowed
// curated near-match (instant, no LLM) or the evidence verifier reading
// retrieved passages. The UI and offline eval distinguish a borrowed verdict
// from a reasoned one by this tag.
type VerdictSource string

const (
	// SourceCurated tags a verdict borrowed from a high-confidence curated
	// near-match on the fast path.
	SourceCurated VerdictSource = "curated"
	// SourceVerified tags a verdict the evidence verifier derived by reading
	// retrieved passages against the claim.
	SourceVerified VerdictSource = "verified"
)

// AtomicClaim is one self-contained claim a unit decomposed into: its stable
// per-claim id (shared across that claim's pending/checking/verified events so
// the client replaces in place) and its coreference-resolved text.
type AtomicClaim struct {
	ClaimID string
	Text    string
}

// VerifiedVerdict is the retrieve-then-verify path's grounded verdict for one
// atomic claim, carried on a LiveEventResult. It is nil for a checking or unchecked
// result.
//
// Verdict is the credibility axis (credible/disputed/unverifiable); Basis is
// evidence (grounded in a surviving citation) or knowledge (the verifier's
// world-knowledge tiebreaker); Confidence comes from the verifier (or the
// near-match similarity for a curated verdict); Citations are the retrieved
// matches the verdict cited (with their evidence ids, so the grounding
// round-trips); Rationale is the one-sentence reason.
//
// Literal and Flags are the political path's two orthogonal axes and are zero on
// the credibility-only path (so a non-political verdict is byte-for-byte the
// legacy shape). Literal is the face-value verdict (accurate/inaccurate/
// unverifiable); Flags is the subset of the closed manipulation vocabulary
// (missing-context, cherry-picked, outdated, misattributed, misleading-causation)
// that applies to the claim's framing, independent of whether the literal claim is
// true. Verdict (the credibility axis) is derived from Literal on the political
// path so the per-speaker tally and the existing frontend verdict contract keep
// working; the frontend (VER-104) reads Literal and Flags for the two-axis
// display.
type VerifiedVerdict struct {
	Verdict    string
	Basis      string
	Confidence float64
	Citations  []domain.SegmentMatch
	Rationale  string
	Literal    string
	Flags      []string
}

// ClaimDecomposer splits one checkable unit into atomic, self-contained claims.
// It is the consumer-side port the retrieve-then-verify path depends on; the
// claimdecomp.Client (via a thin adapter) satisfies it. It never errors: on a
// model failure the implementation degrades to returning the unit verbatim as a
// single claim, so the live path never stalls on decomposition.
type ClaimDecomposer interface {
	Decompose(ctx context.Context, text, speaker, recentContext string) []string
}

// EvidencePassage is one retrieved passage handed to the verifier: its stable
// evidence id (so a citation round-trips) and the passage text the model reads.
type EvidencePassage struct {
	ID   string
	Text string
}

// EvidenceCitation is one grounding the verifier returned: the evidence id it
// relied on and the exact span it quoted, both already validated by the verifier
// adapter's citation guard before they reach the live path.
type EvidenceCitation struct {
	EvidenceID string
	QuotedSpan string
}

// ClaimVerdict is the verifier's grounded credibility judgment for one atomic
// claim: credible/disputed/unverifiable, the grounding basis (evidence or
// knowledge), the verifier's calibrated confidence, the validated citations, and a
// one-sentence rationale.
type ClaimVerdict struct {
	Verdict    string
	Basis      string
	Confidence float64
	Citations  []EvidenceCitation
	Rationale  string
}

// ClaimVerifier judges one atomic claim against retrieved evidence passages and
// returns a grounded verdict with cited spans. It is the consumer-side port the
// verify path depends on; the verify.Client (via a thin adapter) satisfies it.
// It errors when the judgment is unavailable (transport, decode); the verify
// path reports the claim's result with a non-fatal error rather than ending the
// session.
type ClaimVerifier interface {
	Verify(ctx context.Context, claim string, passages []EvidencePassage) (ClaimVerdict, error)
}

// retrieved is the verify path's view of one statement's retrieval: the wire
// matches the verifier's citations attach to, and the query embedding reused for
// consistency. It is the MatchResult minus the corroboration confidence the
// verify path no longer computes (the verdict's confidence comes from the
// verifier instead).
type retrieved struct {
	matches   []domain.SegmentMatch
	embedding []float32
}

// VerifyPathConfig wires a VerifyPath. Decomposer, Matcher, and Verifier are
// required. FastTau is the curated near-match similarity at or above which the
// fast path borrows a verdict without a verify call. VerifyConcurrency bounds
// in-flight verify calls (the verify pool); VerifyQueueDepth bounds the per-unit
// claims waiting for a verify slot before a claim is shed to unchecked.
// FastDeadline bounds one claim's decompose-plus-retrieve fast stage; VerifyDeadline
// bounds one verify call. CacheTTL collapses repeated normalized claims over a
// short window (0 disables the cache). Logger defaults to slog.Default.
type VerifyPathConfig struct {
	Decomposer        ClaimDecomposer
	Matcher           SegmentMatcher
	Verifier          ClaimVerifier
	FastTau           float64
	VerifyConcurrency int
	VerifyQueueDepth  int
	FastDeadline      time.Duration
	VerifyDeadline    time.Duration
	CacheTTL          time.Duration
	Logger            *slog.Logger
	// Political, when non-nil, switches a checkable claim's verify stage onto the
	// political path (FACTCHECK_POLITICAL on): classify -> route+retrieve -> two-axis
	// verify, folding into the flag-aware aggregator. The curated fast-path borrow,
	// the event lifecycle, and the capacity-shed semantics are unchanged. When nil
	// the path runs the credibility-only verify stage, so the political flag off is
	// byte-for-byte the legacy retrieve-then-verify behavior.
	Political *PoliticalConfig
	// SecondPass, when non-nil, enables the deeper-reasoner second pass: after a
	// credibility-only verify call returns an evidence-grounded verdict whose
	// confidence sits in the configured mid band, a stronger reasoning model
	// re-judges the same evidence and may upgrade the verdict, re-emitted in place.
	// It runs after the fast verdict is emitted and outside the verify pool, so it
	// never delays the live result or consumes a scoring slot. When nil (the default)
	// the verify path is byte-for-byte the legacy single-pass behavior.
	SecondPass *SecondPassConfig
}

// VerifyPath is the retrieve-then-verify orchestration for one analyzer: per
// checkable unit it decomposes into atomic claims (on the analyzer's fast pool),
// runs a curated near-match fast path per claim, and falls back to the evidence
// verifier under a bounded verify pool. It owns the verify pool's semaphore and
// the short-TTL claim cache; it holds no transport types.
type VerifyPath struct {
	decomposer     ClaimDecomposer
	matcher        SegmentMatcher
	verifier       ClaimVerifier
	fastTau        float64
	verifySem      chan struct{}
	verifyQueue    int
	fastDeadline   time.Duration
	verifyDeadline time.Duration
	logger         *slog.Logger
	cache          *claimCache
	pol            *PoliticalConfig
	secondPass     *secondPass
}

// NewVerifyPath builds a VerifyPath, failing when a required collaborator is
// missing or a bound is non-positive (a zero verify pool would shed every claim).
func NewVerifyPath(cfg VerifyPathConfig) (*VerifyPath, error) {
	switch {
	case cfg.Decomposer == nil:
		return nil, errors.New("service: verify path requires a decomposer")
	case cfg.Matcher == nil:
		return nil, errors.New("service: verify path requires a matcher")
	case cfg.Verifier == nil:
		return nil, errors.New("service: verify path requires a verifier")
	case cfg.VerifyConcurrency <= 0:
		return nil, fmt.Errorf("service: verify path concurrency must be positive, got %d", cfg.VerifyConcurrency)
	case cfg.VerifyQueueDepth < 0:
		return nil, fmt.Errorf("service: verify path queue depth must be non-negative, got %d", cfg.VerifyQueueDepth)
	case !domain.ValidCosineThreshold(cfg.FastTau):
		return nil, fmt.Errorf("service: verify path fast tau %v outside cosine similarity range [-1, 1]", cfg.FastTau)
	case cfg.FastDeadline <= 0:
		return nil, fmt.Errorf("service: verify path fast deadline must be positive, got %s", cfg.FastDeadline)
	case cfg.VerifyDeadline <= 0:
		return nil, fmt.Errorf("service: verify path verify deadline must be positive, got %s", cfg.VerifyDeadline)
	case cfg.CacheTTL < 0:
		return nil, fmt.Errorf("service: verify path cache ttl must be non-negative, got %s", cfg.CacheTTL)
	}
	if cfg.Political != nil {
		switch {
		case cfg.Political.Classifier == nil:
			return nil, errors.New("service: political verify path requires a classifier")
		case cfg.Political.Retriever == nil:
			return nil, errors.New("service: political verify path requires a retriever")
		case cfg.Political.Verifier == nil:
			return nil, errors.New("service: political verify path requires a two-axis verifier")
		}
	}
	var sp *secondPass
	if cfg.SecondPass != nil {
		var err error
		if sp, err = newSecondPass(*cfg.SecondPass); err != nil {
			return nil, err
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	var cache *claimCache
	if cfg.CacheTTL > 0 {
		cache = newClaimCache(cfg.CacheTTL)
	}
	return &VerifyPath{
		decomposer:     cfg.Decomposer,
		matcher:        cfg.Matcher,
		verifier:       cfg.Verifier,
		fastTau:        cfg.FastTau,
		verifySem:      make(chan struct{}, cfg.VerifyConcurrency),
		verifyQueue:    cfg.VerifyQueueDepth,
		fastDeadline:   cfg.FastDeadline,
		verifyDeadline: cfg.VerifyDeadline,
		logger:         logger,
		cache:          cache,
		pol:            cfg.Political,
		secondPass:     sp,
	}, nil
}

// BatchClaimResult is one atomic claim's resolved verdict produced outside the
// live socket, for the document analyzer. Status is verified or error; a batch
// job never sheds, so there is no unchecked outcome. Source and Verdict are
// zero on an error claim.
type BatchClaimResult struct {
	Claim   AtomicClaim
	Status  ClaimStatus
	Source  VerdictSource
	Verdict *VerifiedVerdict
}

// BatchUnitResult is the outcome of analyzing one text unit (a document
// sentence) in batch. When Checkable is false the gate skipped the unit with
// SkipReason; when Checkable is true but Claims is empty, decomposition found no
// verifiable claim (a not_a_claim skip). Otherwise Claims holds one resolved
// verdict per atomic claim, in decomposition order.
type BatchUnitResult struct {
	Checkable  bool
	SkipReason domain.SkipReason
	Claims     []BatchClaimResult
}

// AnalyzeText runs one text unit through the verify path for a batch analyzer:
// gate -> decompose -> per-claim fast/verify, returning the resolved verdicts
// without emitting live events, speaker tallies, or consistency. Unlike the live
// path it never sheds: each claim's verify call blocks on the verify pool until
// a slot frees (bounded only by ctx), so every claim ends with a real verdict or
// an error. The gate is supplied by the caller; VerifyPath holds none. It errors
// only when the gate itself fails - a per-claim retrieval or verify failure is a
// non-fatal error result, mirroring the live path.
func (vp *VerifyPath) AnalyzeText(ctx context.Context, gate SegmentPrechecker, text, anchorID string) (BatchUnitResult, error) {
	decision, err := gate.Evaluate(ctx, text)
	if err != nil {
		return BatchUnitResult{}, fmt.Errorf("service: batch gate: %w", err)
	}
	if !decision.Checkable {
		return BatchUnitResult{Checkable: false, SkipReason: decision.Reason}, nil
	}

	claims := vp.decomposeText(ctx, text, "", anchorID)
	if len(claims) == 0 {
		return BatchUnitResult{Checkable: true, SkipReason: domain.SkipReasonNotAClaim}, nil
	}

	// A sentence's claims resolve concurrently, bounded by the shared verify
	// pool's semaphore; the analyzer processes sentences one at a time, so the
	// pool is the only concurrency bound. results is index-addressed so the
	// goroutines never contend on a shared slice.
	results := make([]BatchClaimResult, len(claims))
	var wg sync.WaitGroup
	for i, claim := range claims {
		wg.Add(1)
		go func(i int, claim AtomicClaim) {
			defer wg.Done()
			results[i] = vp.resolveClaimBatch(ctx, claim)
		}(i, claim)
	}
	wg.Wait()
	return BatchUnitResult{Checkable: true, Claims: results}, nil
}

// resolveClaimBatch resolves one atomic claim for a batch job. It mirrors
// scoreClaim's branch order - cache, retrieve, political/credibility curated
// borrow, political route+verify or credibility verify - but returns the verdict
// rather than emitting it, blocks on the verify pool instead of shedding, and
// has no unchecked outcome. A retrieval or verify failure is an error result.
func (vp *VerifyPath) resolveClaimBatch(ctx context.Context, claim AtomicClaim) BatchClaimResult {
	if cached, ok := vp.cacheGet(claim.Text); ok {
		return BatchClaimResult{Claim: claim, Status: ClaimStatusVerified, Source: cached.source, Verdict: cached.verdict}
	}

	ret, err := vp.retrieve(ctx, claim.Text)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "batch retrieval failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		return BatchClaimResult{Claim: claim, Status: ClaimStatusError}
	}

	// Political curated fast borrow (political path only, ahead of the legacy
	// borrow because only it carries the manipulation axis).
	if vp.political() {
		if verdict, ok := vp.politicalFastMatch(ctx, ret.embedding); ok {
			vp.cachePut(claim.Text, SourceCurated, verdict, ret.embedding)
			return BatchClaimResult{Claim: claim, Status: ClaimStatusVerified, Source: SourceCurated, Verdict: verdict}
		}
	}

	// Credibility curated fast borrow: a high-similarity curated near-match
	// borrows its verdict with no verifier call, carrying its literal axis on the
	// political path.
	if fast, ok := vp.fastMatch(ret.matches); ok {
		verdict := curatedVerdict(fast)
		if vp.political() {
			verdict.Literal = literalFromCredibility(verdict.Verdict)
		}
		vp.cachePut(claim.Text, SourceCurated, verdict, ret.embedding)
		return BatchClaimResult{Claim: claim, Status: ClaimStatusVerified, Source: SourceCurated, Verdict: verdict}
	}

	if vp.political() {
		return vp.resolvePoliticalClaimBatch(ctx, claim, ret)
	}

	verdict, err := vp.verifyClaimBlocking(ctx, claim.Text, ret.matches)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "batch verifier failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		return BatchClaimResult{Claim: claim, Status: ClaimStatusError}
	}
	vp.cachePut(claim.Text, SourceVerified, verdict, ret.embedding)
	return BatchClaimResult{Claim: claim, Status: ClaimStatusVerified, Source: SourceVerified, Verdict: verdict}
}

// scoreUnit is the verify-path replacement for the legacy LiveAnalyzer.scoreUnit:
// gate the unit, decompose it into atomic claims, announce them, then run each
// claim's fast/verify lifecycle concurrently under the verify pool. It reuses the
// analyzer's gate (a.prechecker) and per-speaker consistency machinery; the
// matcher and verifier are its own. A non-checkable unit emits the legacy skip
// result per member, so a not_a_claim unit looks the same as on the old path.
func (vp *VerifyPath) scoreUnit(ctx context.Context, a *LiveAnalyzer, out chan<- LiveEvent, mem *speakerMemory, pu pendingUnit) {
	members := pu.members
	text := combinedText(members)

	decision, err := a.prechecker.Evaluate(ctx, text)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "verify-path gate failed", slog.String("ids", memberIDs(members)), slog.Any("err", err))
		}
		vp.emitMemberErrors(ctx, out, members)
		return
	}
	if !decision.Checkable {
		// A gate skip mirrors the legacy result shape so a not_a_claim unit is
		// reported identically whether or not the verify path is on.
		for _, m := range members {
			if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: m.id, Segment: m.seg, SkipReason: decision.Reason}) {
				return
			}
		}
		return
	}

	claims := vp.decompose(ctx, pu, text)
	if len(claims) == 0 {
		// Decomposition dropped every fragment as non-factual: the unit carried no
		// verifiable claim, so it is a single not_a_claim skip rather than a fan-out.
		for _, m := range members {
			if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: m.id, Segment: m.seg, SkipReason: domain.SkipReasonNotAClaim}) {
				return
			}
		}
		return
	}

	unitID := members[0].id
	unitSeg := members[0].seg
	if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventClaims, ID: unitID, Segment: unitSeg, Claims: claims}) {
		return
	}

	// Per-claim verify calls run concurrently, capped by the verify pool so one
	// multi-claim unit cannot starve the next speaker. The per-claim lifecycle
	// emits to a shared out channel; sendEvent is the only writer contention and
	// is itself ctx-aware.
	var wg sync.WaitGroup
	for _, claim := range claims {
		wg.Add(1)
		go func(claim AtomicClaim) {
			defer wg.Done()
			vp.scoreClaim(ctx, a, out, mem, pu, claim)
		}(claim)
	}
	wg.Wait()
}

// decompose runs the unit through the decomposer on the fast pool and assigns
// each surviving atomic claim a stable per-unit claim id (the unit's anchor id
// plus an index), so the client keys a claim's pending/checking/verified events
// together. The decomposer never errors; an empty result means the unit carried
// no verifiable claim.
func (vp *VerifyPath) decompose(ctx context.Context, pu pendingUnit, text string) []AtomicClaim {
	return vp.decomposeText(ctx, text, pu.speaker, pu.members[0].id)
}

// decomposeText is the transport-free core of decompose: it runs the decomposer
// on the fast pool and assigns each surviving atomic claim a stable id
// (anchorID plus an index). The live path passes the unit's anchor id and
// speaker; the batch analyzer passes a per-sentence anchor and an empty
// speaker. Behavior is identical to the prior inline body.
func (vp *VerifyPath) decomposeText(ctx context.Context, text, speaker, anchorID string) []AtomicClaim {
	fastCtx, cancel := context.WithTimeout(ctx, vp.fastDeadline)
	defer cancel()
	texts := vp.decomposer.Decompose(fastCtx, text, speaker, "")
	claims := make([]AtomicClaim, 0, len(texts))
	for i, t := range texts {
		claims = append(claims, AtomicClaim{ClaimID: anchorID + "-" + strconv.Itoa(i), Text: t})
	}
	return claims
}

// scoreClaim runs one atomic claim's lifecycle: fast curated near-match, then
// the evidence verifier under the verify pool, emitting per-claim results keyed
// on claim.ClaimID so the client replaces in place. A cache hit short-circuits a
// repeated claim. Retrieval and verdict errors are reported as the non-fatal
// error terminal status rather than ending the session.
func (vp *VerifyPath) scoreClaim(ctx context.Context, a *LiveAnalyzer, out chan<- LiveEvent, mem *speakerMemory, pu pendingUnit, claim AtomicClaim) {
	unitID := pu.members[0].id
	seg := pu.members[0].seg

	if cached, ok := vp.cacheGet(claim.Text); ok {
		vp.emitVerdict(ctx, out, unitID, claim, seg, cached.source, cached.verdict)
		vp.recordSpeakerTally(ctx, out, mem, pu.speaker, cached.verdict)
		vp.recordConsistency(ctx, a, out, mem, pu, claim, cached.embedding)
		return
	}

	ret, err := vp.retrieve(ctx, claim.Text)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "verify-path retrieval failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		vp.emitClaimError(ctx, out, unitID, claim, seg)
		return
	}

	// Political curated fast-path: a recurring talking point that near-matches a
	// curated two-axis political claim borrows its full verdict (literal verdict +
	// manipulation flags + real source) with no LLM call. It runs only on the
	// political path and ahead of the legacy credibility borrow below, because only
	// this borrow can carry the manipulation axis (the legacy borrow asserts no flag).
	// A miss falls through to the legacy fast match and then route+verify, so the
	// flag-off path is byte-for-byte unchanged.
	if vp.political() {
		if verdict, ok := vp.politicalFastMatch(ctx, ret.embedding); ok {
			vp.cachePut(claim.Text, SourceCurated, verdict, ret.embedding)
			vp.emitVerdict(ctx, out, unitID, claim, seg, SourceCurated, verdict)
			vp.recordSpeakerTally(ctx, out, mem, pu.speaker, verdict)
			vp.recordConsistency(ctx, a, out, mem, pu, claim, ret.embedding)
			return
		}
	}

	// Fast path: a high-confidence curated near-match borrows its verdict with no
	// LLM call, tagged curated, and emitted at once. The borrow is shared across the
	// credibility and political paths; on the political path the curated verdict also
	// carries its literal axis (curatedVerdict maps corroborates -> accurate).
	if fast, ok := vp.fastMatch(ret.matches); ok {
		verdict := curatedVerdict(fast)
		if vp.political() {
			// On the political path a borrowed verdict also carries its literal axis,
			// derived from the credibility verdict the curated corpus already settled
			// (corroborates -> accurate, contradicts -> inaccurate); a curated borrow
			// asserts no manipulation flag. The credibility-only path leaves Literal
			// zero, keeping its wire shape unchanged.
			verdict.Literal = literalFromCredibility(verdict.Verdict)
		}
		vp.cachePut(claim.Text, SourceCurated, verdict, ret.embedding)
		vp.emitVerdict(ctx, out, unitID, claim, seg, SourceCurated, verdict)
		vp.recordSpeakerTally(ctx, out, mem, pu.speaker, verdict)
		vp.recordConsistency(ctx, a, out, mem, pu, claim, ret.embedding)
		return
	}

	// Political path: the curated borrow was ruled out, so classify, route+retrieve,
	// and two-axis verify, folding into the flag-aware aggregator. It owns its own
	// checking/verified/unchecked/error lifecycle from here.
	if vp.political() {
		vp.scorePoliticalClaim(ctx, a, out, mem, pu, claim, ret)
		return
	}

	// Verify path: announce checking, then run the verifier under the bounded
	// pool. Pool saturation sheds the claim to unchecked rather than dropping it.
	// Every per-claim frame carries ID=unit anchor and ClaimID=claim id, so id
	// always means the unit and claim_id exclusively identifies the claim.
	if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: unitID, Segment: seg, ClaimID: claim.ClaimID, ClaimStatus: ClaimStatusChecking}) {
		return
	}
	verdict, shed, err := vp.verifyClaim(ctx, claim.Text, ret.matches)
	if shed {
		_ = sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: unitID, Segment: seg, ClaimID: claim.ClaimID, ClaimStatus: ClaimStatusUnchecked, SkipReason: domain.SkipReasonNotChecked})
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "verify-path verifier failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		vp.emitClaimError(ctx, out, unitID, claim, seg)
		return
	}
	vp.cachePut(claim.Text, SourceVerified, verdict, ret.embedding)
	vp.emitVerdict(ctx, out, unitID, claim, seg, SourceVerified, verdict)
	vp.recordSpeakerTally(ctx, out, mem, pu.speaker, verdict)
	vp.recordConsistency(ctx, a, out, mem, pu, claim, ret.embedding)
	// Deeper second pass, off by default: a grounded mid-confidence verdict gets
	// re-judged by a stronger reasoning model and may be upgraded in place. It runs
	// only after the fast verdict has already emitted above and outside the verify
	// pool, so it never delays the live result or consumes a scoring slot; it is a
	// no-op when the feature is off or the verdict does not qualify.
	vp.maybeReverify(ctx, out, pu, claim, verdict, ret)
}

// retrieve embeds the atomic claim and pulls the high-recall evidence cluster
// under the fast deadline (the spec's fast tier is embedding + ANN). The claim
// text is the retrieval query, not the raw unit: a coreference-resolved claim is
// a cleaner query. It returns the wire matches and the query embedding, reused
// for consistency.
func (vp *VerifyPath) retrieve(ctx context.Context, claim string) (retrieved, error) {
	fastCtx, cancel := context.WithTimeout(ctx, vp.fastDeadline)
	defer cancel()
	result, err := vp.matcher.Match(fastCtx, claim)
	if err != nil {
		return retrieved{}, err
	}
	return retrieved{matches: result.Matches, embedding: result.QueryEmbedding}, nil
}

// fastMatch returns the strongest curated claim hit at or above the fast tau, if
// any. Only curated claims carry a borrowable verdict; wiki evidence is never a
// fast-path verdict. Matches arrive nearest-first, so the first qualifying claim
// is the strongest.
func (vp *VerifyPath) fastMatch(matches []domain.SegmentMatch) (domain.SegmentMatch, bool) {
	for _, m := range matches {
		if m.Kind != domain.MatchKindClaim {
			continue
		}
		if m.Similarity >= vp.fastTau {
			return m, true
		}
	}
	return domain.SegmentMatch{}, false
}

// verifyClaim runs the verifier under the verify pool. It returns shed=true when
// the pool and its bounded queue are saturated, so the caller emits the honest
// unchecked terminal state rather than blocking the unit. With no passages it
// returns a not_enough_info verdict without a verify call: a verdict with no
// evidence is meaningless and the verifier would reject it.
func (vp *VerifyPath) verifyClaim(ctx context.Context, claim string, matches []domain.SegmentMatch) (*VerifiedVerdict, bool, error) {
	if len(matches) == 0 {
		// No retrieved evidence and no model call: the honest "nothing to check
		// against" outcome is unverifiable, not a low-confidence judgment.
		return &VerifiedVerdict{Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0}, false, nil
	}
	if !vp.acquireVerifySlot(ctx) {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, nil
	}
	defer func() { <-vp.verifySem }()

	verdict, err := vp.runVerifier(ctx, claim, matches)
	return verdict, false, err
}

// runVerifier runs the credibility verifier against retrieved evidence under an
// already-held verify slot (the caller owns slot acquisition and release). It
// bounds the call by the verify deadline and resolves the verifier's citations
// back to the retrieved matches. Split out of verifyClaim so the live
// shed-acquire path and the batch blocking-acquire path share one verifier body.
func (vp *VerifyPath) runVerifier(ctx context.Context, claim string, matches []domain.SegmentMatch) (*VerifiedVerdict, error) {
	verifyCtx, cancel := context.WithTimeout(ctx, vp.verifyDeadline)
	defer cancel()

	passages := passagesFromMatches(matches)
	res, err := vp.verifier.Verify(verifyCtx, claim, passages)
	if err != nil {
		return nil, err
	}
	return verdictFromResult(res, matches), nil
}

// verifyClaimBlocking is the no-shed counterpart of verifyClaim for a batch job:
// it blocks on the verify pool until a slot frees (bounded only by ctx) rather
// than shedding to unchecked, so every claim ends with a real verdict. The
// no-evidence short-circuit and the verifier body are shared with verifyClaim.
func (vp *VerifyPath) verifyClaimBlocking(ctx context.Context, claim string, matches []domain.SegmentMatch) (*VerifiedVerdict, error) {
	if len(matches) == 0 {
		return &VerifiedVerdict{Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0}, nil
	}
	if !vp.acquireVerifySlotBlocking(ctx) {
		return nil, ctx.Err()
	}
	defer func() { <-vp.verifySem }()
	return vp.runVerifier(ctx, claim, matches)
}

// acquireVerifySlot takes a verify-pool slot, waiting up to the bounded queue's
// worth of patience. With queue depth 0 it is a non-blocking try; a positive
// depth lets a claim wait for a freeing slot under a short bounded poll so a
// brief saturation buffers rather than sheds. It reports false when the pool
// stays saturated (the claim is shed) or ctx is canceled.
func (vp *VerifyPath) acquireVerifySlot(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case vp.verifySem <- struct{}{}:
		return true
	default:
	}
	if vp.verifyQueue == 0 {
		return false
	}
	// A bounded wait turns a brief burst past the pool into a queue rather than an
	// immediate shed; the deadline keeps a sustained overload from stalling the
	// claim indefinitely. The window scales with the verify deadline and queue
	// depth but is capped at two verify deadlines so a deep queue cannot defer an
	// honest capacity shed for many pool-cycles.
	window := min(vp.verifyDeadline*time.Duration(vp.verifyQueue), 2*vp.verifyDeadline)
	wait := time.NewTimer(window)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		return false
	case vp.verifySem <- struct{}{}:
		return true
	case <-wait.C:
		return false
	}
}

// acquireVerifySlotBlocking waits for a verify-pool slot without ever shedding:
// a batch document job can wait, so it blocks until a slot frees or ctx is
// canceled. It reports false only on cancellation. This is the backpressure the
// batch analyzer applies in place of the live path's capacity shed.
func (vp *VerifyPath) acquireVerifySlotBlocking(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case vp.verifySem <- struct{}{}:
		return true
	}
}

// recordConsistency runs intra-speaker consistency for one atomic claim, off the
// critical path: it compares the claim against the speaker's prior atomic claims
// (reusing the claim's retrieval embedding) and remembers it. It is a no-op when
// the feature is off or the claim carries no reusable embedding.
func (vp *VerifyPath) recordConsistency(ctx context.Context, a *LiveAnalyzer, out chan<- LiveEvent, mem *speakerMemory, pu pendingUnit, claim AtomicClaim, embedding []float32) {
	a.detectClaimConsistency(ctx, out, mem, pu.speaker, claim, embedding, pu.members[0].seg)
}

// emitVerdict sends one claim's verified result. ID is the unit anchor and
// ClaimID identifies the claim, so id always means the unit and the client keys
// the row by claim_id. The verdict's citations are surfaced as the result's wire
// matches so the verifier's evidence round-trips to the UI.
func (vp *VerifyPath) emitVerdict(ctx context.Context, out chan<- LiveEvent, unitID string, claim AtomicClaim, seg domain.Segment, source VerdictSource, verdict *VerifiedVerdict) {
	_ = sendEvent(ctx, out, LiveEvent{
		Kind:        LiveEventResult,
		ID:          unitID,
		Segment:     seg,
		ClaimID:     claim.ClaimID,
		ClaimStatus: ClaimStatusVerified,
		Source:      source,
		Matches:     verdict.Citations,
		Verdict:     verdict,
	})
}

// emitClaimError reports a non-fatal per-claim failure (retrieval or verifier)
// as the error terminal status, distinct from a reached verdict, so one bad
// claim never ends the session and the client does not mistake it for a verdict.
// ID is the unit anchor and ClaimID identifies the claim.
func (vp *VerifyPath) emitClaimError(ctx context.Context, out chan<- LiveEvent, unitID string, claim AtomicClaim, seg domain.Segment) {
	_ = sendEvent(ctx, out, LiveEvent{
		Kind:        LiveEventResult,
		ID:          unitID,
		Segment:     seg,
		ClaimID:     claim.ClaimID,
		ClaimStatus: ClaimStatusError,
		Err:         "verification failed",
	})
}

// emitMemberErrors reports a unit-level gate failure as a non-fatal error per
// member, matching the legacy path's unit-failure shape.
func (vp *VerifyPath) emitMemberErrors(ctx context.Context, out chan<- LiveEvent, members []unitMember) {
	for _, m := range members {
		if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: m.id, Segment: m.seg, Err: "analysis failed"}) {
			return
		}
	}
}

// passagesFromMatches projects the retrieved wire matches into verifier
// passages, dropping any match without an evidence id (it cannot be cited and
// the citation guard would reject it).
func passagesFromMatches(matches []domain.SegmentMatch) []EvidencePassage {
	passages := make([]EvidencePassage, 0, len(matches))
	for _, m := range matches {
		if m.EvidenceID == "" {
			continue
		}
		passages = append(passages, EvidencePassage{ID: m.EvidenceID, Text: m.Claim})
	}
	return passages
}

// verdictFromResult builds the wire verdict from the verifier's judgment,
// resolving each validated citation back to the retrieved match it grounds (by
// evidence id) so the UI shows the cited passage rather than a bare id. A
// citation whose id is not among the retrieved matches is dropped (the guard
// already rejected fabricated ids, so this only guards a defensive mismatch).
func verdictFromResult(res ClaimVerdict, matches []domain.SegmentMatch) *VerifiedVerdict {
	byID := make(map[string]domain.SegmentMatch, len(matches))
	for _, m := range matches {
		if m.EvidenceID != "" {
			byID[m.EvidenceID] = m
		}
	}
	citations := make([]domain.SegmentMatch, 0, len(res.Citations))
	for _, c := range res.Citations {
		if m, ok := byID[c.EvidenceID]; ok {
			citations = append(citations, m)
		}
	}
	return &VerifiedVerdict{
		Verdict:    res.Verdict,
		Basis:      res.Basis,
		Confidence: res.Confidence,
		Citations:  citations,
		Rationale:  res.Rationale,
	}
}

// curatedVerdict maps a borrowed curated near-match into a credibility verdict on
// the fast path. The curated claim is the cited source, so a corroborating curated
// verdict is credible and a contradicting one is disputed, both basis evidence; an
// unclear curated claim does not settle the speaker's statement, so it is
// unverifiable and (per the unverifiable invariant) carries no citation. The
// borrowed confidence is the near-match similarity.
func curatedVerdict(fast domain.SegmentMatch) *VerifiedVerdict {
	state, basis := credibilityFromCurated(fast.Verdict)
	verdict := &VerifiedVerdict{Verdict: state, Basis: basis, Confidence: fast.Similarity}
	if state != VerdictUnverifiable {
		verdict.Citations = []domain.SegmentMatch{fast}
	}
	return verdict
}

// credibilityFromCurated maps the curated corpus verdict vocabulary
// (corroborates/contradicts/unclear) onto the credibility vocabulary. A curated
// borrow is always evidence-grounded (the curated claim is the source) except an
// unclear one, which grounds nothing and is unverifiable.
func credibilityFromCurated(v domain.Verdict) (state, basis string) {
	switch v {
	case domain.VerdictCorroborates:
		return VerdictCredible, BasisEvidence
	case domain.VerdictContradicts:
		return VerdictDisputed, BasisEvidence
	default:
		return VerdictUnverifiable, BasisKnowledge
	}
}

// recordSpeakerTally folds one claim's reached verdict into the speaker's running
// tally and emits the updated snapshot as a LiveEventSpeakerTally. It is a no-op
// for an unattributed turn (no speaker to tally) or a nil verdict
// (checking/unchecked/error claims never reach here). Each of credible, disputed,
// and unverifiable moves its own count. On the political path a verdict carrying at
// least one manipulation flag also bumps the orthogonal misleading-framing tally,
// folded under one lock so the emitted snapshot is internally consistent. The same
// recorder serves the curated-borrow, cache-hit, verified, and political branches,
// so the framing axis can never be silently dropped on a replay.
func (vp *VerifyPath) recordSpeakerTally(ctx context.Context, out chan<- LiveEvent, mem *speakerMemory, speaker string, verdict *VerifiedVerdict) {
	if speaker == "" || verdict == nil {
		return
	}
	tally := mem.observeVerdict(speaker, verdict.Verdict, len(verdict.Flags) > 0)
	_ = sendEvent(ctx, out, LiveEvent{Kind: LiveEventSpeakerTally, SpeakerTally: &tally})
}

// cacheEntry is one cached claim verdict: its source tag, the verdict, and the
// retrieval embedding, so a cache hit can still feed consistency detection
// without re-embedding.
type cacheEntry struct {
	source    VerdictSource
	verdict   *VerifiedVerdict
	embedding []float32
	expires   time.Time
}

// claimCache is the short-TTL cache that collapses repeated normalized claims
// (recurring debate talking points) so they are not re-verified within the
// window. It is safe for concurrent use across the per-claim goroutines.
type claimCache struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[string]cacheEntry
	now func() time.Time
}

func newClaimCache(ttl time.Duration) *claimCache {
	return &claimCache{ttl: ttl, m: make(map[string]cacheEntry), now: time.Now}
}

// get returns a live entry for the normalized claim, evicting an expired one.
func (c *claimCache) get(claim string) (cacheEntry, bool) {
	key := normalizeClaim(claim)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return cacheEntry{}, false
	}
	if c.now().After(e.expires) {
		delete(c.m, key)
		return cacheEntry{}, false
	}
	return e, true
}

func (c *claimCache) put(claim string, e cacheEntry) {
	key := normalizeClaim(claim)
	e.expires = c.now().Add(c.ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = e
}

// cacheGet/cachePut are nil-safe wrappers so a disabled cache (nil) is a no-op
// without a guard at every call site.
func (vp *VerifyPath) cacheGet(claim string) (cacheEntry, bool) {
	if vp.cache == nil {
		return cacheEntry{}, false
	}
	return vp.cache.get(claim)
}

func (vp *VerifyPath) cachePut(claim string, source VerdictSource, verdict *VerifiedVerdict, embedding []float32) {
	if vp.cache == nil {
		return
	}
	vp.cache.put(claim, cacheEntry{source: source, verdict: verdict, embedding: embedding})
}

// normalizeClaim folds case and collapses internal whitespace so two phrasings
// of the same talking point share a cache key. It is deterministic and
// allocation-light.
func normalizeClaim(claim string) string {
	return strings.Join(strings.Fields(strings.ToLower(claim)), " ")
}
