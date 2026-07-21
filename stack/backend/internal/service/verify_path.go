package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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
// the client replaces in place) and its coreference-resolved text. Quote is the
// verbatim run of source-statement words the claim came from, and Spans locates
// those words inside the unit's member segments, so the client can highlight
// the exact words that were checked. Both are empty when the decomposer could
// not anchor the claim (the claim is still verified, just not highlighted).
type AtomicClaim struct {
	ClaimID string
	Text    string
	Quote   string
	Spans   []domain.ClaimSpan
}

// DecomposedClaim is one claim the decomposer extracted: the self-contained
// claim text the verifier judges and the verbatim quote of the source words it
// was extracted from (empty when the model failed to copy a verbatim span).
type DecomposedClaim struct {
	Text  string
	Quote string
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

// ClaimDecomposer splits one checkable unit into atomic, self-contained claims,
// each paired with the verbatim quote of the unit words it came from. It is the
// consumer-side port the retrieve-then-verify path depends on; the
// claimdecomp.Client (via a thin adapter) satisfies it. It never errors: on a
// model failure the implementation degrades to returning the unit verbatim as a
// single claim, so the live path never stalls on decomposition.
type ClaimDecomposer interface {
	Decompose(ctx context.Context, text, speaker, recentContext string) []DecomposedClaim
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
// bounds one verify call. CacheTTL is the window over which the semantic cache
// collapses a repeated or paraphrased claim (embedding-cosine keyed, negation-polarity
// guarded) onto its recent verdict; 0 disables the cache. Logger defaults to slog.Default.
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
	// CacheThreshold is the cosine-similarity bar the semantic claim cache
	// requires before it replays a cached verdict for a new claim's embedding, and
	// CacheMaxEntries bounds the cache's size (oldest evicted first). Both matter
	// only when CacheTTL is positive; a zero CacheMaxEntries defaults to
	// defaultCacheMaxEntries and an out-of-range CacheThreshold fails construction.
	CacheThreshold  float64
	CacheMaxEntries int
	Logger          *slog.Logger
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
	cache          *semanticCache
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
	case cfg.CacheTTL > 0 && !domain.ValidCosineThreshold(cfg.CacheThreshold):
		return nil, fmt.Errorf("service: verify path cache threshold %v outside cosine similarity range [-1, 1]", cfg.CacheThreshold)
	case cfg.CacheTTL > 0 && cfg.CacheMaxEntries < 0:
		return nil, fmt.Errorf("service: verify path cache max entries must be non-negative, got %d", cfg.CacheMaxEntries)
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
	var cache *semanticCache
	if cfg.CacheTTL > 0 {
		maxEntries := cfg.CacheMaxEntries
		if maxEntries == 0 {
			maxEntries = defaultCacheMaxEntries
		}
		cache = newSemanticCache(cfg.CacheTTL, cfg.CacheThreshold, maxEntries)
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
// without emitting live events, speaker tallies, or consistency. recentContext
// is the prior text the decomposer resolves references against (the preceding
// group of document sentences); it is context only and never decomposed itself. Unlike
// the live path it never sheds: each claim's verify call blocks on the verify
// pool until a slot frees (bounded only by ctx), so every claim ends with a
// real verdict or an error. The gate is supplied by the caller; VerifyPath
// holds none. It errors only when the gate itself fails - a per-claim retrieval
// or verify failure is a non-fatal error result, mirroring the live path.
func (vp *VerifyPath) AnalyzeText(ctx context.Context, gate SegmentPrechecker, text, recentContext, anchorID string) (BatchUnitResult, error) {
	decision, err := gate.Evaluate(ctx, text)
	if err != nil {
		return BatchUnitResult{}, fmt.Errorf("service: batch gate: %w", err)
	}
	if !decision.Checkable {
		return BatchUnitResult{Checkable: false, SkipReason: decision.Reason}, nil
	}

	claims := vp.decomposeText(ctx, text, "", recentContext, anchorID)
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
	ret, err := vp.retrieve(ctx, claim.Text)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "batch retrieval failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		return BatchClaimResult{Claim: claim, Status: ClaimStatusError}
	}

	// The semantic cache is keyed on the claim's query embedding, so the lookup
	// happens after retrieval has produced it: a paraphrase of a recently verified
	// claim embeds near the original and replays its verdict with no verifier call.
	if cached, ok := vp.cacheGet(ret.embedding, claim.Text); ok {
		return BatchClaimResult{Claim: claim, Status: ClaimStatusVerified, Source: cached.source, Verdict: cached.verdict}
	}

	// Political curated fast borrow (political path only, ahead of the legacy
	// borrow because only it carries the manipulation axis).
	if vp.political() {
		if verdict, ok := vp.politicalFastMatch(ctx, ret.embedding); ok {
			vp.cachePut(ret.embedding, claim.Text, SourceCurated, verdict)
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
		vp.cachePut(ret.embedding, claim.Text, SourceCurated, verdict)
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
	// Apply the deeper-reasoner second pass, same as the live credibility path,
	// so a document verdict matches what live would show for the same sentence.
	// It is a no-op when the feature is off or the verdict does not qualify.
	upgraded := vp.applyReverifyBatch(ctx, claim, verdict, ret)
	vp.cachePut(ret.embedding, claim.Text, SourceVerified, upgraded)
	return BatchClaimResult{Claim: claim, Status: ClaimStatusVerified, Source: SourceVerified, Verdict: upgraded}
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
	segmentIDs := make([]string, len(members))
	for i, m := range members {
		segmentIDs[i] = m.id
	}
	if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventClaims, ID: unitID, Segment: unitSeg, Claims: claims, SegmentIDs: segmentIDs}) {
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
// together. It passes the unit's recent context (the previous unit's full
// speaker-labeled text, bounded) so the decomposer resolves cross-unit
// references, and anchors each claim's verbatim quote onto the member segments
// as highlight spans. The decomposer never errors; an empty result means the
// unit carried no verifiable claim.
func (vp *VerifyPath) decompose(ctx context.Context, pu pendingUnit, text string) []AtomicClaim {
	claims := vp.decomposeText(ctx, text, pu.speaker, pu.context, pu.members[0].id)
	for i := range claims {
		claims[i].Spans = claimSpans(pu.members, text, claims[i].Quote)
	}
	return claims
}

// decomposeText is the transport-free core of decompose: it runs the decomposer
// on the fast pool and assigns each surviving atomic claim a stable id
// (anchorID plus an index). The live path passes the unit's anchor id, speaker,
// and recent context; the batch analyzer passes a per-sentence anchor, an empty
// speaker, and the previous sentence as context. Spans are the caller's
// concern: only the live path knows the unit's member segments.
func (vp *VerifyPath) decomposeText(ctx context.Context, text, speaker, recentContext, anchorID string) []AtomicClaim {
	fastCtx, cancel := context.WithTimeout(ctx, vp.fastDeadline)
	defer cancel()
	decomposed := vp.decomposer.Decompose(fastCtx, text, speaker, recentContext)
	claims := make([]AtomicClaim, 0, len(decomposed))
	for i, d := range decomposed {
		claims = append(claims, AtomicClaim{ClaimID: anchorID + "-" + strconv.Itoa(i), Text: d.Text, Quote: d.Quote})
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

	ret, err := vp.retrieve(ctx, claim.Text)
	if err != nil {
		if ctx.Err() == nil {
			vp.logger.ErrorContext(ctx, "verify-path retrieval failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
		}
		vp.emitClaimError(ctx, out, unitID, claim, seg)
		return
	}

	// The semantic cache keys on the claim's query embedding, so the lookup runs
	// after retrieval has produced it: a paraphrased repeat of a recent claim
	// embeds near the original and replays its cached verdict with no verifier
	// call. Consistency reuses this claim's own fresh embedding, not the cached
	// one, so the stance comparison is against the exact statement just spoken.
	if cached, ok := vp.cacheGet(ret.embedding, claim.Text); ok {
		vp.emitVerdict(ctx, out, unitID, claim, seg, cached.source, cached.verdict)
		vp.recordSpeakerTally(ctx, out, mem, pu.speaker, cached.verdict)
		vp.recordConsistency(ctx, a, out, mem, pu, claim, ret.embedding)
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
			vp.cachePut(ret.embedding, claim.Text, SourceCurated, verdict)
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
		vp.cachePut(ret.embedding, claim.Text, SourceCurated, verdict)
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
	vp.cachePut(ret.embedding, claim.Text, SourceVerified, verdict)
	vp.emitVerdict(ctx, out, unitID, claim, seg, SourceVerified, verdict)
	vp.recordSpeakerTally(ctx, out, mem, pu.speaker, verdict)
	vp.recordConsistency(ctx, a, out, mem, pu, claim, ret.embedding)
	// Terminal reasoning gate, off by default: a weak verdict (unverifiable, or below
	// the trigger floor) gets re-judged by a stronger reasoning model and may be
	// upgraded in place, with the speaker tally moved to match. It runs only after the
	// fast verdict has already emitted above and outside the verify pool, so it never
	// delays the live result or consumes a scoring slot; it is a no-op when the feature
	// is off or the verdict is already strong.
	vp.maybeReverify(ctx, out, mem, pu, claim, verdict, ret)
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
// unchecked terminal state rather than blocking the unit. With no passages there
// is nothing to judge against: the claim short-circuits to the unverifiable
// no-evidence verdict without a verify call or a pool slot - a verdict may only
// be credible or disputed when retrieved evidence backs it.
func (vp *VerifyPath) verifyClaim(ctx context.Context, claim string, matches []domain.SegmentMatch) (verdict *VerifiedVerdict, shed bool, err error) {
	if len(matches) == 0 {
		return noEvidenceVerdict(), false, nil
	}
	if !vp.acquireVerifySlot(ctx) {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, nil
	}
	defer func() { <-vp.verifySem }()

	verdict, err = vp.runVerifier(ctx, claim, matches)
	return verdict, false, err
}

// noSourceRationale is the viewer-facing explanation carried by every verdict
// that no source could settle. Rationales are always French, whatever the
// configured locale.
const noSourceRationale = "Aucune source n'a pu être trouvée pour vérifier cette affirmation."

// noEvidenceVerdict is the instant outcome for a claim that retrieved nothing:
// unverifiable on a knowledge basis with zero confidence and the fixed French
// no-source rationale.
func noEvidenceVerdict() *VerifiedVerdict {
	return &VerifiedVerdict{Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0, Rationale: noSourceRationale}
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
// no-evidence short-circuit is shared with verifyClaim.
func (vp *VerifyPath) verifyClaimBlocking(ctx context.Context, claim string, matches []domain.SegmentMatch) (*VerifiedVerdict, error) {
	if len(matches) == 0 {
		return noEvidenceVerdict(), nil
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
// A judgment that no retrieved source backs - basis knowledge, or an evidence
// basis whose every citation failed to resolve - is demoted to unverifiable:
// credible and disputed are reserved for verdicts at least one real source
// grounds. The model's rationale is kept so the viewer still reads why, and
// per the unverifiable invariant the demoted verdict carries no citations.
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
	if res.Basis != BasisEvidence || len(citations) == 0 {
		// Confidence is zeroed like noEvidenceVerdict's: it measured the model's
		// certainty in a judgment no source backs, and rendering it next to
		// "unverifiable" would read as a confident verdict.
		return &VerifiedVerdict{
			Verdict:   VerdictUnverifiable,
			Basis:     BasisKnowledge,
			Rationale: res.Rationale,
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

// recordSpeakerReTally corrects the speaker tally after the terminal gate upgraded a
// claim's verdict: it moves the claim from its prior credibility bucket to the new one
// and emits a fresh tally snapshot, so the aggregate breakdown stays consistent with the
// upgraded per-claim verdict the viewer now sees. It is the tally counterpart of a gate
// upgrade; recordSpeakerTally already counted the fast verdict, so this moves that single
// count rather than adding a second (no double-count). It is a no-op when the credibility
// bucket is unchanged (a same-bucket confidence bump), so a gate that only sharpens
// confidence emits no redundant tally frame. The misleading-framing tally needs no
// correction: a gate upgrade carries the claim's manipulation flags through unchanged,
// so the framing count the fast verdict set still holds.
func (vp *VerifyPath) recordSpeakerReTally(ctx context.Context, out chan<- LiveEvent, mem *speakerMemory, speaker string, old, upgraded *VerifiedVerdict) {
	if speaker == "" || old == nil || upgraded == nil || old.Verdict == upgraded.Verdict {
		return
	}
	tally := mem.reobserveVerdict(speaker, old.Verdict, upgraded.Verdict)
	_ = sendEvent(ctx, out, LiveEvent{Kind: LiveEventSpeakerTally, SpeakerTally: &tally})
}

// defaultCacheMaxEntries bounds the semantic cache when a caller enables the
// cache (CacheTTL > 0) but leaves CacheMaxEntries zero. It mirrors the env
// layer's default so a direct construction and the wired stack agree.
const defaultCacheMaxEntries = 1024

// cacheEntry is one cached claim verdict: its source tag, the verdict, the
// retrieval embedding that keys it, and the negation-polarity of the claim it was
// verified for. A lookup scores a new claim's embedding against the key and a hit
// can still feed consistency detection without re-embedding; negated vetoes a hit
// whose polarity disagrees with the query (see negationVeto below).
type cacheEntry struct {
	source    VerdictSource
	verdict   *VerifiedVerdict
	embedding []float32
	negated   bool
	expires   time.Time
}

// semanticCache collapses a repeated or paraphrased claim onto a recent verdict
// without a fresh verifier call: it keys entries on the claim's query embedding
// and, on lookup, returns the cached verdict whose embedding is nearest the new
// claim's when that cosine similarity clears a configurable bar. A high bar keeps
// a genuinely different claim from borrowing an unrelated verdict (the false-share
// guard), and a negation-polarity veto (get's negated argument) rejects a hit
// where one claim is a negation and the other is not - the case dense embeddings
// famously blur ("le chomage a augmente" vs "le chomage n'a pas augmente" sit well
// above the bar yet carry opposite verdicts). The TTL bounds staleness and the
// size bound (oldest evicted first) keeps a long session from growing it without
// limit. It is safe for concurrent use across the per-claim goroutines.
type semanticCache struct {
	ttl        time.Duration
	threshold  float64
	maxEntries int
	mu         sync.Mutex
	// entries is ordered oldest-first (append on put), so eviction pops the front
	// and the scan visits every live entry. The cache is small and bounded, so a
	// linear scan is cheaper than a vector index.
	entries []cacheEntry
	now     func() time.Time
}

func newSemanticCache(ttl time.Duration, threshold float64, maxEntries int) *semanticCache {
	return &semanticCache{ttl: ttl, threshold: threshold, maxEntries: maxEntries, now: time.Now}
}

// get returns the live cached entry whose embedding is most similar to vec when
// that similarity clears the threshold, evicting expired entries it passes.
// negated is the query claim's negation polarity: an entry whose polarity differs
// is vetoed even above the bar, so a negated claim never replays its affirmation's
// verdict (or vice versa). A nil or empty vec never matches (cosine is 0), so a
// claim with no reusable embedding falls through to a fresh verify rather than
// false-sharing.
func (c *semanticCache) get(vec []float32, negated bool) (cacheEntry, bool) {
	if len(vec) == 0 {
		return cacheEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	best := -1
	bestSim := c.threshold
	live := c.entries[:0]
	for _, e := range c.entries {
		if now.After(e.expires) {
			continue
		}
		live = append(live, e)
	}
	c.entries = live
	for i, e := range c.entries {
		// Negation-polarity veto: a disagreeing polarity is skipped no matter how
		// close the embedding, so an affirmation can never replay a negation's
		// verdict. A false veto is a safe cache miss; a false share is a wrong verdict.
		if e.negated != negated {
			continue
		}
		sim := cosineSimilarity(vec, e.embedding)
		if sim >= bestSim {
			// >= keeps the last-seen (more recent) entry on an exact tie, and the
			// running bar rises to the best so the nearest cached claim wins.
			best = i
			bestSim = sim
		}
	}
	if best < 0 {
		return cacheEntry{}, false
	}
	return c.entries[best], true
}

// put stores an entry keyed on its embedding with a fresh expiry. An entry whose
// embedding exactly matches a live one REPLACES it in place rather than appending,
// so a claim that goes verify -> terminal-gate upgrade (both cached under the same
// retrieval embedding) holds one slot carrying the upgraded verdict, not two. Once
// the size bound is reached the oldest entries are evicted. An entry with no
// embedding is dropped: it could never be matched and, at a non-positive
// threshold, could false-share against another empty-vector claim.
func (c *semanticCache) put(e cacheEntry) {
	if len(e.embedding) == 0 {
		return
	}
	e.expires = c.now().Add(c.ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.entries {
		if slices.Equal(c.entries[i].embedding, e.embedding) {
			c.entries[i] = e
			return
		}
	}
	c.entries = append(c.entries, e)
	if c.maxEntries > 0 && len(c.entries) > c.maxEntries {
		// Drop the oldest overflow in one shift so a burst that overshoots the bound
		// does not leave the slice permanently over capacity.
		drop := len(c.entries) - c.maxEntries
		c.entries = append(c.entries[:0], c.entries[drop:]...)
	}
}

// negationMarkers are the word-boundary negation tokens whose presence in one
// claim but not the other vetoes a semantic cache hit. Dense embeddings sit a
// negated claim and its affirmation well above the cache threshold, so replaying
// one verdict for the other would emit the OPPOSITE credibility/political verdict -
// unacceptable for a fact-checker. The set is French + English, case-folded (every
// marker is ASCII, so accents in surrounding words are irrelevant). "plus" is
// deliberately excluded: it negates only in "ne...plus", already caught by the
// "ne"/"n'" markers, and counting it alone would veto the common affirmative
// "plus de X" ("more X"), needlessly killing legitimate cache hits.
var negationMarkers = map[string]struct{}{
	// French
	"ne": {}, "pas": {}, "non": {}, "jamais": {}, "aucun": {}, "aucune": {}, "ni": {}, "sans": {},
	// English
	"not": {}, "no": {}, "never": {}, "without": {}, "neither": {}, "nor": {},
}

// hasNegation reports whether text carries a negation marker, case-folding and
// normalizing the curly apostrophe so French elision ("n'a", "n'est") and English
// contractions ("n't", as in "isn't"/"don't") are detected alongside the standalone
// markers. It is deliberately conservative: over-detecting negation only forces a
// safe cache miss, while under-detecting risks replaying an opposite verdict.
func hasNegation(text string) bool {
	folded := strings.ReplaceAll(strings.ToLower(text), "’", "'")
	for _, tok := range strings.FieldsFunc(folded, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	}) {
		if _, ok := negationMarkers[tok]; ok {
			return true
		}
		if strings.HasPrefix(tok, "n'") || strings.Contains(tok, "n't") {
			return true
		}
	}
	return false
}

// cacheGet/cachePut are nil-safe wrappers so a disabled cache (nil) is a no-op
// without a guard at every call site. They key on the claim's retrieval
// embedding, so a lookup happens after retrieval has produced the vector, and
// carry the claim text so the cache can veto a negation-polarity mismatch.
func (vp *VerifyPath) cacheGet(vec []float32, claim string) (cacheEntry, bool) {
	if vp.cache == nil {
		return cacheEntry{}, false
	}
	return vp.cache.get(vec, hasNegation(claim))
}

func (vp *VerifyPath) cachePut(embedding []float32, claim string, source VerdictSource, verdict *VerifiedVerdict) {
	if vp.cache == nil {
		return
	}
	vp.cache.put(cacheEntry{source: source, verdict: verdict, embedding: embedding, negated: hasNegation(claim)})
}
