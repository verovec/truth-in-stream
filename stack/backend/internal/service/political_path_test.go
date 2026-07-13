package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/claimtype"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// fakeClassifier returns the claim type keyed by claim text, defaulting to a
// verifiable type so an unmapped claim still routes, matching the real
// classifier's degrade-to-DefaultType contract.
type fakeClassifier struct {
	byClaim map[string]claimtype.Type
}

func (c fakeClassifier) Classify(_ context.Context, claim string) claimtype.Type {
	if ct, ok := c.byClaim[claim]; ok {
		return ct
	}
	return claimtype.DefaultType
}

// fakeRouterRetriever returns source evidence keyed by claim text and records the
// claim types it was asked to route, so a test can assert routing happened.
type fakeRouterRetriever struct {
	byClaim map[string][]source.Evidence
	err     map[string]error

	mu       sync.Mutex
	routedCT []claimtype.Type
}

func (r *fakeRouterRetriever) Retrieve(_ context.Context, claim string, ct claimtype.Type, _ map[string]string) ([]source.Evidence, error) {
	r.mu.Lock()
	r.routedCT = append(r.routedCT, ct)
	r.mu.Unlock()
	if err := r.err[claim]; err != nil {
		return nil, err
	}
	return r.byClaim[claim], nil
}

func (r *fakeRouterRetriever) routed() []claimtype.Type {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]claimtype.Type(nil), r.routedCT...)
}

// fakePoliticalVerifier returns a two-axis verdict keyed by claim text and records
// every call. A blocking release channel pins it so a test can saturate the pool.
type fakePoliticalVerifier struct {
	byClaim map[string]PoliticalVerdict
	err     map[string]error
	release <-chan struct{}

	mu    sync.Mutex
	calls []string
}

func (v *fakePoliticalVerifier) VerifyPolitical(ctx context.Context, claim string, _ []EvidencePassage) (PoliticalVerdict, error) {
	v.mu.Lock()
	v.calls = append(v.calls, claim)
	v.mu.Unlock()
	if v.release != nil {
		select {
		case <-v.release:
		case <-ctx.Done():
			return PoliticalVerdict{}, ctx.Err()
		}
	}
	if err := v.err[claim]; err != nil {
		return PoliticalVerdict{}, err
	}
	return v.byClaim[claim], nil
}

func (v *fakePoliticalVerifier) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.calls...)
}

// srcEvidence builds one source.Evidence passage with a stable evidence id.
func srcEvidence(kind source.Kind, sourceID, passage string) source.Evidence {
	return source.Evidence{
		ID:      source.NewEvidenceID(kind, sourceID, 0),
		Passage: passage,
		Source:  source.Source{Name: "INSEE", URL: "https://insee.fr/x", Date: "2024"},
	}
}

// politicalFixture builds a LiveAnalyzer wired to the political verify path with
// sensible test defaults.
func politicalFixture(t *testing.T, stream SegmentStream, matcher SegmentMatcher, vpCfg VerifyPathConfig, pol PoliticalConfig) *LiveAnalyzer {
	t.Helper()
	if vpCfg.Matcher == nil {
		vpCfg.Matcher = matcher
	}
	if vpCfg.FastTau == 0 {
		vpCfg.FastTau = 0.85
	}
	if vpCfg.VerifyConcurrency == 0 {
		vpCfg.VerifyConcurrency = 2
	}
	if vpCfg.FastDeadline == 0 {
		vpCfg.FastDeadline = time.Second
	}
	if vpCfg.VerifyDeadline == 0 {
		vpCfg.VerifyDeadline = time.Second
	}
	vpCfg.Political = &pol
	vp, err := NewVerifyPath(vpCfg)
	if err != nil {
		t.Fatalf("NewVerifyPath: %v", err)
	}
	a, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     stream,
		Matcher:    matcher,
		Prechecker: allowAllPrechecker{},
		Logger:     discardLogger(),
		Verify:     vp,
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}
	return a
}

func TestPoliticalPathEventLifecycleCarriesLiteralFlagsSource(t *testing.T) {
	t.Parallel()
	// Behind the political path, one unit decomposes into one atomic claim that is
	// classified, routed, retrieved, and two-axis verified: the verdict carries the
	// literal axis, the manipulation flags, and the verified source. Asserts the
	// full lifecycle Subtitle -> Claims(pending) -> Result(checking) ->
	// Result(verified) with literal + flags + source on the terminal result.
	unit := "le chômage a baissé de 2 points."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A",
	})}
	// No curated near-match: the curated matcher returns nothing, so the claim takes
	// the routed political verify path rather than a fast borrow.
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{}}
	classifier := fakeClassifier{byClaim: map[string]claimtype.Type{unit: claimtype.Statistic}}
	router := &fakeRouterRetriever{byClaim: map[string][]source.Evidence{
		unit: {srcEvidence(source.KindStatsINSEE, "CHOMAGE-T", "le taux de chômage est passé de 7,5% à 7,3% sur un an")},
	}}
	verifier := &fakePoliticalVerifier{byClaim: map[string]PoliticalVerdict{
		unit: {
			Literal:    LiteralAccurate,
			Basis:      BasisEvidence,
			Flags:      []string{FlagCherryPicked},
			Confidence: 0.9,
			Citations:  []EvidenceCitation{{EvidenceID: source.NewEvidenceID(source.KindStatsINSEE, "CHOMAGE-T", 0).String(), QuotedSpan: "7,5% à 7,3%"}},
			Rationale:  "le chiffre est exact mais la période est choisie",
		},
	}}

	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {unit}}},
		Verifier:   &fakeVerifier{}, // never used on the political path
	}, PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier})

	events := runVerifyPath(t, a)

	if events[0].Kind != LiveEventSubtitle || events[0].Segment.Text != unit {
		t.Fatalf("first event = %+v, want subtitle for unit", events[0])
	}
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil || len(claimsEv.Claims) != 1 {
		t.Fatalf("claims event = %+v, want one claim", claimsEv)
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (checking, verified)", len(results))
	}
	if results[0].ClaimStatus != ClaimStatusChecking {
		t.Fatalf("first result status = %q, want checking", results[0].ClaimStatus)
	}
	last := results[1]
	if last.ClaimStatus != ClaimStatusVerified || last.Source != SourceVerified {
		t.Fatalf("terminal result = status %q source %q, want verified/verified", last.ClaimStatus, last.Source)
	}
	if last.Verdict == nil {
		t.Fatal("terminal result carries no verdict")
	}
	if last.Verdict.Literal != LiteralAccurate {
		t.Fatalf("literal = %q, want accurate", last.Verdict.Literal)
	}
	if len(last.Verdict.Flags) != 1 || last.Verdict.Flags[0] != FlagCherryPicked {
		t.Fatalf("flags = %v, want [cherry-picked]", last.Verdict.Flags)
	}
	// The cited evidence round-trips to a wire match carrying the source provenance.
	if len(last.Verdict.Citations) != 1 {
		t.Fatalf("citations = %+v, want one", last.Verdict.Citations)
	}
	cit := last.Verdict.Citations[0]
	if cit.EvidenceID != source.NewEvidenceID(source.KindStatsINSEE, "CHOMAGE-T", 0).String() {
		t.Fatalf("citation evidence id = %q, want the routed source id", cit.EvidenceID)
	}
	if len(cit.Sources) != 1 || cit.Sources[0].Title != "INSEE" {
		t.Fatalf("citation source = %+v, want INSEE provenance", cit.Sources)
	}
	// The classifier's type drove routing.
	if routed := router.routed(); len(routed) != 1 || routed[0] != claimtype.Statistic {
		t.Fatalf("routed types = %v, want [statistic]", routed)
	}
}

// TestPoliticalPathTerminalGateUpgradesWeakVerdict proves the terminal gate now
// applies to the political two-axis path (the old unconditional skip is gone): a weak
// two-axis verdict (literal unverifiable) fires the deeper credibility reasoner over
// the routed evidence, and a grounded high-confidence re-judgment maps back onto the
// literal axis (disputed -> inaccurate) and re-emits in place, after the fast verdict
// already emitted, for the same claim id.
func TestPoliticalPathTerminalGateUpgradesWeakVerdict(t *testing.T) {
	t.Parallel()
	unit := "l'immigration a fait exploser la délinquance."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{}}
	classifier := fakeClassifier{byClaim: map[string]claimtype.Type{unit: claimtype.Statistic}}
	evID := source.NewEvidenceID(source.KindStatsINSEE, "DELINQ", 0).String()
	router := &fakeRouterRetriever{byClaim: map[string][]source.Evidence{
		unit: {srcEvidence(source.KindStatsINSEE, "DELINQ", "aucune corrélation établie entre immigration et délinquance")},
	}}
	// The fast two-axis verifier is unsure: literal unverifiable, low confidence.
	verifier := &fakePoliticalVerifier{byClaim: map[string]PoliticalVerdict{
		unit: {Literal: LiteralUnverifiable, Basis: BasisKnowledge, Confidence: 0.3, Rationale: "insuffisant"},
	}}
	// The deeper reasoner grounds a disputed credibility verdict at high confidence.
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.95, Citations: []EvidenceCitation{{EvidenceID: evID, QuotedSpan: "aucune corrélation"}}, Rationale: "réfuté par la source"},
	}}

	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {unit}}},
		Verifier:   &fakeVerifier{},
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	}, PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil {
		t.Fatal("no claims event")
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3 (checking, weak verified, gated upgrade)", len(results))
	}
	if results[1].Verdict == nil || results[1].Verdict.Literal != LiteralUnverifiable {
		t.Fatalf("fast verdict = %+v, want literal unverifiable emitted first", results[1].Verdict)
	}
	up := results[2]
	if up.ClaimStatus != ClaimStatusVerified || up.Source != SourceVerified {
		t.Fatalf("gated result = status %q source %q, want verified/verified", up.ClaimStatus, up.Source)
	}
	if up.Verdict == nil {
		t.Fatal("gated result carries no verdict")
	}
	if up.Verdict.Literal != LiteralInaccurate {
		t.Fatalf("gated literal = %q, want inaccurate (disputed -> inaccurate)", up.Verdict.Literal)
	}
	if up.Verdict.Verdict != VerdictDisputed {
		t.Fatalf("gated credibility = %q, want disputed", up.Verdict.Verdict)
	}
	if up.Verdict.Basis != BasisEvidence || up.Verdict.Confidence != 0.95 {
		t.Fatalf("gated verdict = %+v, want grounded at 0.95", up.Verdict)
	}
	if len(up.Verdict.Citations) != 1 || len(up.Verdict.Citations[0].Sources) != 1 || up.Verdict.Citations[0].Sources[0].Title != "INSEE" {
		t.Fatalf("gated citations = %+v, want one INSEE citation", up.Verdict.Citations)
	}
	if seen := reverifier.seen(); len(seen) != 1 {
		t.Fatalf("reverifier calls = %v, want exactly one (the weak verdict fired the gate)", seen)
	}
}

func TestPoliticalPathTalliesMoveAndFramingTallyMoves(t *testing.T) {
	t.Parallel()
	// The flag-aware aggregator: an accurate claim bumps the credible count, an
	// inaccurate claim bumps the disputed count, and a flagged claim moves the
	// misleading-framing tally independently of the literal verdict.
	unit := "trois affirmations."
	accClaim, inaccClaim, flaggedClaim := "fait exact.", "fait faux.", "fait exact mais trompeur."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A",
	})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{}}
	classifier := fakeClassifier{}
	mk := func(id string) []source.Evidence {
		return []source.Evidence{srcEvidence(source.KindWebSearch, id, "passage")}
	}
	router := &fakeRouterRetriever{byClaim: map[string][]source.Evidence{
		accClaim: mk("h1"), inaccClaim: mk("h2"), flaggedClaim: mk("h3"),
	}}
	verifier := &fakePoliticalVerifier{byClaim: map[string]PoliticalVerdict{
		accClaim: {
			Literal: LiteralAccurate, Basis: BasisEvidence, Confidence: 0.8,
			Citations: []EvidenceCitation{{EvidenceID: source.NewEvidenceID(source.KindWebSearch, "h1", 0).String(), QuotedSpan: "passage"}},
		},
		inaccClaim: {
			Literal: LiteralInaccurate, Basis: BasisEvidence, Confidence: 0.8,
			Citations: []EvidenceCitation{{EvidenceID: source.NewEvidenceID(source.KindWebSearch, "h2", 0).String(), QuotedSpan: "passage"}},
		},
		flaggedClaim: {
			Literal: LiteralAccurate, Basis: BasisEvidence, Confidence: 0.8, Flags: []string{FlagMissingContext},
			Citations: []EvidenceCitation{{EvidenceID: source.NewEvidenceID(source.KindWebSearch, "h3", 0).String(), QuotedSpan: "passage"}},
		},
	}}

	// Concurrency and queue depth cover all three claims so none is shed for
	// capacity: every claim reaches a verdict and emits its tally, making the
	// cumulative final snapshot deterministic regardless of completion order.
	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer:        fakeDecomposer{byText: map[string][]string{unit: {accClaim, inaccClaim, flaggedClaim}}},
		Verifier:          &fakeVerifier{},
		VerifyConcurrency: 3,
		VerifyQueueDepth:  3,
	}, PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier})

	events := runVerifyPath(t, a)

	tallies := speakerTallies(events)
	if len(tallies) != 3 {
		t.Fatalf("speaker tally events = %d, want 3 (one per reached verdict)", len(tallies))
	}
	final := maxSampleTally(tallies)
	// accurate -> credible, inaccurate -> disputed, the flagged accurate -> credible
	// too: two credible, one disputed.
	if final.Credible != 2 || final.Disputed != 1 {
		t.Fatalf("tallies = credible %d disputed %d, want 2/1", final.Credible, final.Disputed)
	}
	// Exactly one claim carried a flag, so the misleading-framing tally is 1.
	if final.MisleadingFraming != 1 {
		t.Fatalf("misleading framing = %d, want 1", final.MisleadingFraming)
	}
}

func TestPoliticalPathFastCuratedBorrowStillInstant(t *testing.T) {
	t.Parallel()
	// The curated fast path survives the political wiring: a claim with a curated
	// near-match at or above tau borrows the verdict with no classify/route/verify
	// call, tagged curated, mapping corroborates -> accurate.
	unit := "une affirmation déjà vérifiée."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A",
	})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		unit: {{Kind: domain.MatchKindClaim, Claim: "source curée", Verdict: domain.VerdictCorroborates, Similarity: 0.95, EvidenceID: "claim:c1:0"}},
	}}
	classifier := fakeClassifier{}
	router := &fakeRouterRetriever{}
	verifier := &fakePoliticalVerifier{}

	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {unit}}},
		Verifier:   &fakeVerifier{},
	}, PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil || len(claimsEv.Claims) != 1 {
		t.Fatalf("claims event = %+v, want one claim", claimsEv)
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 1 || results[0].ClaimStatus != ClaimStatusVerified || results[0].Source != SourceCurated {
		t.Fatalf("results = %+v, want single verified/curated", results)
	}
	if results[0].Verdict == nil || results[0].Verdict.Literal != LiteralAccurate {
		t.Fatalf("curated verdict literal = %+v, want accurate", results[0].Verdict)
	}
	if seen := verifier.seen(); len(seen) != 0 {
		t.Fatalf("political verifier called %v, want none on a curated borrow", seen)
	}
	if routed := router.routed(); len(routed) != 0 {
		t.Fatalf("router called %v, want none on a curated borrow", routed)
	}
}

func TestPoliticalPathShedsToUncheckedOnCapacity(t *testing.T) {
	t.Parallel()
	// More claims than the verify pool can run, no queue: the excess sheds to the
	// honest unchecked terminal state rather than dropping or stalling.
	unit := "claim one. claim two. claim three."
	c1, c2, c3 := "claim one fact.", "claim two fact.", "claim three fact."
	stream := pausingStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{}}
	classifier := fakeClassifier{}
	mk := func(id string) []source.Evidence {
		return []source.Evidence{srcEvidence(source.KindWebSearch, id, "p")}
	}
	router := &fakeRouterRetriever{byClaim: map[string][]source.Evidence{c1: mk("a"), c2: mk("b"), c3: mk("c")}}
	release := make(chan struct{})
	verifier := &fakePoliticalVerifier{byClaim: map[string]PoliticalVerdict{}, release: release}

	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer:        fakeDecomposer{byText: map[string][]string{unit: {c1, c2, c3}}},
		Verifier:          &fakeVerifier{},
		VerifyConcurrency: 1,
		VerifyQueueDepth:  0,
		VerifyDeadline:    50 * time.Millisecond,
	}, PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier})

	ctx, cancel := context.WithCancel(context.Background())
	out, err := a.Run(ctx, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var events []LiveEvent
	deadline := time.After(2 * time.Second)
	sawUnchecked := false
	for !sawUnchecked {
		select {
		case ev, ok := <-out:
			if !ok {
				t.Fatal("stream closed before a capacity shed was observed")
			}
			events = append(events, ev)
			if ev.Kind == LiveEventResult && ev.ClaimStatus == ClaimStatusUnchecked && ev.SkipReason == domain.SkipReasonNotChecked {
				sawUnchecked = true
			}
		case <-deadline:
			t.Fatalf("timed out without a capacity shed; events so far: %d", len(events))
		}
	}
	close(release)
	cancel()
	for range out { //nolint:revive // drain to completion
	}
}

func TestPoliticalPathVerifierErrorEmitsErrorTerminal(t *testing.T) {
	t.Parallel()
	// A two-axis verifier failure ends in the error terminal status, distinct from a
	// reached verdict, so one bad claim never ends the session.
	unit := "une affirmation."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{}}
	classifier := fakeClassifier{}
	router := &fakeRouterRetriever{byClaim: map[string][]source.Evidence{unit: {srcEvidence(source.KindWebSearch, "h", "p")}}}
	verifier := &fakePoliticalVerifier{err: map[string]error{unit: errors.New("verifier boom")}}

	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {unit}}},
		Verifier:   &fakeVerifier{},
	}, PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil || len(claimsEv.Claims) != 1 {
		t.Fatalf("claims event = %+v, want one claim", claimsEv)
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	last := results[len(results)-1]
	if last.ClaimStatus != ClaimStatusError || last.Err == "" || last.Verdict != nil {
		t.Fatalf("terminal = %+v, want error with no verdict", last)
	}
}

func TestPoliticalPathRoutingFailureWithNoEvidenceIsUnverifiable(t *testing.T) {
	t.Parallel()
	// When routing returns no evidence (and no error), the claim short-circuits to
	// unverifiable/knowledge without a verifier call: the honest "nothing to check
	// against" outcome, the same as the credibility path's no-evidence case.
	unit := "un avis subjectif sans source."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{}}
	classifier := fakeClassifier{}
	router := &fakeRouterRetriever{byClaim: map[string][]source.Evidence{}} // empty, no error
	verifier := &fakePoliticalVerifier{}

	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {unit}}},
		Verifier:   &fakeVerifier{},
	}, PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil || len(claimsEv.Claims) != 1 {
		t.Fatalf("claims event = %+v, want one claim", claimsEv)
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	// A routed claim shows checking -> verified even when retrieval is empty, the
	// same lifecycle position as the credibility path, so a client keys its row
	// transition the same way regardless of whether evidence was found.
	if len(results) != 2 || results[0].ClaimStatus != ClaimStatusChecking {
		t.Fatalf("results = %+v, want checking then verified", results)
	}
	last := results[len(results)-1]
	if last.ClaimStatus != ClaimStatusVerified || last.Verdict == nil {
		t.Fatalf("terminal = %+v, want a verified verdict", last)
	}
	if last.Verdict.Literal != LiteralUnverifiable {
		t.Fatalf("no-evidence literal = %q, want unverifiable", last.Verdict.Literal)
	}
	if seen := verifier.seen(); len(seen) != 0 {
		t.Fatalf("verifier called %v, want none with no evidence", seen)
	}
}

func TestPoliticalPathCacheReplayPreservesFramingTally(t *testing.T) {
	t.Parallel()
	// A flagged political verdict cached on first sight still bumps the
	// misleading-framing tally when the identical claim recurs within the TTL and is
	// served from the cache: the cache-hit branch records through the same flag-aware
	// recorder, so a recurring flagged talking point is never silently under-counted.
	// Two different speakers keep the two utterances in separate analysis units (a
	// same-speaker pair would merge into one unit), so the second is a distinct claim
	// that hits the cache. The shared speaker for the tally would defeat the point;
	// instead both speak the identical talking point and the tally is asserted on the
	// speaker that says it twice across the cache boundary - here the same normalized
	// claim recurs, and we assert the framing tally on whichever speaker the replay
	// scored. To keep the speaker fixed across both occurrences while still forcing
	// separate units, a third neutral speaker breaks the run between them.
	claim := "le chiffre exact mais trompeur."
	u2 := "Le chiffre exact mais trompeur."
	stream := &fakeSegmentStream{transcripts: finalize(
		domain.Segment{Start: time.Second, End: 2 * time.Second, Text: claim, Speaker: "A"},
		domain.Segment{Start: 3 * time.Second, End: 4 * time.Second, Text: "autre chose.", Speaker: "B"},
		domain.Segment{Start: 5 * time.Second, End: 6 * time.Second, Text: u2, Speaker: "A"},
	)}
	u1 := claim
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{}}
	classifier := fakeClassifier{}
	router := &fakeRouterRetriever{byClaim: map[string][]source.Evidence{
		u1: {srcEvidence(source.KindWebSearch, "h1", "passage")},
		u2: {srcEvidence(source.KindWebSearch, "h1", "passage")},
	}}
	verifier := &fakePoliticalVerifier{byClaim: map[string]PoliticalVerdict{
		u1: {
			Literal: LiteralAccurate, Basis: BasisEvidence, Confidence: 0.8, Flags: []string{FlagCherryPicked},
			Citations: []EvidenceCitation{{EvidenceID: source.NewEvidenceID(source.KindWebSearch, "h1", 0).String(), QuotedSpan: "passage"}},
		},
	}}

	vpCfg := VerifyPathConfig{
		Decomposer:        fakeDecomposer{},
		Verifier:          &fakeVerifier{},
		Matcher:           matcher,
		FastTau:           0.85,
		VerifyConcurrency: 1,
		FastDeadline:      time.Second,
		VerifyDeadline:    time.Second,
		CacheTTL:          time.Minute,
		Political:         &PoliticalConfig{Classifier: classifier, Retriever: router, Verifier: verifier},
	}
	vp, err := NewVerifyPath(vpCfg)
	if err != nil {
		t.Fatalf("NewVerifyPath: %v", err)
	}
	// Concurrency 1 scores the two units sequentially so the first populates the
	// cache before the second looks it up.
	a, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream: stream, Matcher: matcher, Prechecker: allowAllPrechecker{}, Logger: discardLogger(), Concurrency: 1, Verify: vp,
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	events := runVerifyPath(t, a)

	// The verifier ran once; the second occurrence was served from the cache.
	if seen := verifier.seen(); len(seen) != 1 {
		t.Fatalf("verifier called %d times, want 1 (second served from cache)", len(seen))
	}
	// Both occurrences fold their flag into the framing tally, so the final snapshot
	// counts two flagged claims even though only one was verified.
	final := maxSampleTally(speakerTallies(events))
	if final.MisleadingFraming != 2 {
		t.Fatalf("misleading framing = %d, want 2 (cache replay must not drop the framing axis)", final.MisleadingFraming)
	}
	if final.Credible != 2 {
		t.Fatalf("credible = %d, want 2", final.Credible)
	}
}
