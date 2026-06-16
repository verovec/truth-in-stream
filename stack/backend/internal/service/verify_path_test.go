package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeDecomposer splits a unit into the claims keyed by its text; an unknown
// unit falls back to the verbatim unit as a single claim, matching the real
// decomposer's error-degradation contract.
type fakeDecomposer struct {
	byText map[string][]string
}

func (d fakeDecomposer) Decompose(_ context.Context, text, _, _ string) []string {
	if claims, ok := d.byText[text]; ok {
		return claims
	}
	return []string{text}
}

// fakeVerifier returns the verdict keyed by claim text and records every call,
// so a test can assert which claims reached the verify path. A blocking release
// channel pins the verifier so a test can saturate the verify pool.
type fakeVerifier struct {
	byClaim map[string]ClaimVerdict
	err     map[string]error
	release <-chan struct{}

	mu    sync.Mutex
	calls []string
}

func (v *fakeVerifier) Verify(ctx context.Context, claim string, _ []EvidencePassage) (ClaimVerdict, error) {
	v.mu.Lock()
	v.calls = append(v.calls, claim)
	v.mu.Unlock()
	if v.release != nil {
		select {
		case <-v.release:
		case <-ctx.Done():
			return ClaimVerdict{}, ctx.Err()
		}
	}
	if err := v.err[claim]; err != nil {
		return ClaimVerdict{}, err
	}
	return v.byClaim[claim], nil
}

func (v *fakeVerifier) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.calls...)
}

// verifyPathFixture builds a LiveAnalyzer wired to the verify path with sensible
// test defaults, returning it ready to Run.
func verifyPathFixture(t *testing.T, stream SegmentStream, matcher SegmentMatcher, vpCfg VerifyPathConfig) *LiveAnalyzer {
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

func TestLiveAnalyzerFlagOffEmitsLegacyShape(t *testing.T) {
	t.Parallel()
	// With Verify nil (FACTCHECK_VERIFY_PATH off), a checkable unit takes the
	// legacy single-pool gate-and-match path: one subtitle and one result per
	// statement, the result keyed on the subtitle id with no claim fields and no
	// claims event. This is the byte-for-byte unchanged proof for the flag-off path.
	unit := "the earth is round."
	claimMatch := []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Claim: "earth is an oblate spheroid", Verdict: domain.Verdict("corroborates"), Sources: []domain.Source{}, Similarity: 0.9}}
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{
		matches:    map[string][]domain.SegmentMatch{unit: claimMatch},
		confidence: map[string]domain.Confidence{unit: {Score: 0.9, Supporting: 0.9, EvidenceItems: 1}},
	}
	a, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     stream,
		Matcher:    matcher,
		Prechecker: allowAllPrechecker{},
		Logger:     discardLogger(),
		// Verify intentionally nil: the legacy path.
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}
	events := runVerifyPath(t, a)

	if firstOfKind(events, LiveEventClaims) != nil {
		t.Fatal("flag-off path must never emit a claims event")
	}
	subtitles, results := collectByKind(events)
	if len(subtitles) != 1 {
		t.Fatalf("subtitles = %d, want 1", len(subtitles))
	}
	result, ok := results[subtitles[0].ID]
	if !ok {
		t.Fatalf("no result keyed to subtitle id %q", subtitles[0].ID)
	}
	if result.ClaimID != "" || result.ClaimStatus != "" || result.Source != "" || result.Verdict != nil {
		t.Fatalf("flag-off result carries verify-path fields: %+v", result)
	}
	if result.Confidence == nil || result.Confidence.Score != 0.9 {
		t.Fatalf("flag-off result confidence = %+v, want legacy 0.9 score", result.Confidence)
	}
}

func TestVerifyPathEventLifecycle(t *testing.T) {
	t.Parallel()
	// One unit decomposes into two atomic claims; one borrows a curated verdict
	// (fast path, source curated), the other goes through the verifier (source
	// verified). Asserts the lifecycle: Subtitle -> Claims(pending) ->
	// Result(checking) -> Result(verified), with a stable claim_id across updates.
	unit := "the earth is round and the moon is made of cheese."
	fastClaim := "the earth is round."
	verifyClaim := "the moon is made of cheese."

	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A",
	})}
	matcher := liveMatcher{
		matches: map[string][]domain.SegmentMatch{
			fastClaim:   {{Kind: domain.MatchKindClaim, Claim: "earth is an oblate spheroid", Verdict: domain.Verdict("corroborates"), Similarity: 0.95, EvidenceID: "claim:c1:0"}},
			verifyClaim: {{Kind: domain.MatchKindEvidence, Claim: "the moon is composed of rock", Similarity: 0.7, EvidenceID: "evidence:42:0"}},
		},
	}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		verifyClaim: {Verdict: "refutes", Confidence: 0.9, Citations: []EvidenceCitation{{EvidenceID: "evidence:42:0", QuotedSpan: "rock"}}, Rationale: "the moon is rock, not cheese"},
	}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {fastClaim, verifyClaim}}},
		Verifier:   verifier,
	})

	events := runVerifyPath(t, a)

	// Subtitle first, with the unit's text.
	if events[0].Kind != LiveEventSubtitle || events[0].Segment.Text != unit {
		t.Fatalf("first event = %+v, want subtitle for unit", events[0])
	}
	// Claims event announces both atomic claims, pending.
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil {
		t.Fatal("no claims event emitted")
	}
	if len(claimsEv.Claims) != 2 {
		t.Fatalf("claims = %d, want 2", len(claimsEv.Claims))
	}
	fastID, verifyID := claimsEv.Claims[0].ClaimID, claimsEv.Claims[1].ClaimID
	if fastID == verifyID || fastID == "" {
		t.Fatalf("claim ids not distinct/stable: %q %q", fastID, verifyID)
	}

	// Fast claim: a single verified result, source curated, no checking phase.
	fastResults := resultsForClaim(events, fastID)
	if len(fastResults) != 1 {
		t.Fatalf("fast claim results = %d, want 1 (verified only)", len(fastResults))
	}
	if fastResults[0].ClaimStatus != ClaimStatusVerified || fastResults[0].Source != SourceCurated {
		t.Fatalf("fast result = status %q source %q, want verified/curated", fastResults[0].ClaimStatus, fastResults[0].Source)
	}

	// Verify claim: checking then verified, source verified, same claim_id.
	verifyResults := resultsForClaim(events, verifyID)
	if len(verifyResults) != 2 {
		t.Fatalf("verify claim results = %d, want 2 (checking, verified)", len(verifyResults))
	}
	if verifyResults[0].ClaimStatus != ClaimStatusChecking {
		t.Fatalf("first verify result status = %q, want checking", verifyResults[0].ClaimStatus)
	}
	if verifyResults[1].ClaimStatus != ClaimStatusVerified || verifyResults[1].Source != SourceVerified {
		t.Fatalf("final verify result = status %q source %q, want verified/verified", verifyResults[1].ClaimStatus, verifyResults[1].Source)
	}
	if verifyResults[1].Verdict == nil || verifyResults[1].Verdict.Verdict != "refutes" {
		t.Fatalf("verify verdict = %+v, want refutes", verifyResults[1].Verdict)
	}
	// The cited evidence round-trips: the verdict's citation resolves to the
	// retrieved match carrying that evidence id.
	if len(verifyResults[1].Verdict.Citations) != 1 || verifyResults[1].Verdict.Citations[0].EvidenceID != "evidence:42:0" {
		t.Fatalf("verify citations = %+v, want one evidence:42:0", verifyResults[1].Verdict.Citations)
	}
}

func TestVerifyPathFastCuratedNearMatchEmitsImmediately(t *testing.T) {
	t.Parallel()
	// A claim with a curated hit at or above tau_fast borrows the verdict with no
	// verify call, tagged curated, and never enters the checking phase.
	unit := "water boils at 100 degrees celsius."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		unit: {{Kind: domain.MatchKindClaim, Claim: "water boils at 100C at sea level", Verdict: domain.Verdict("corroborates"), Similarity: 0.9, EvidenceID: "claim:c2:0"}},
	}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Verifier:   verifier,
		FastTau:    0.85,
	})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil || len(claimsEv.Claims) != 1 {
		t.Fatalf("claims event = %+v, want one claim", claimsEv)
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 1 || results[0].ClaimStatus != ClaimStatusVerified || results[0].Source != SourceCurated {
		t.Fatalf("results = %+v, want single verified/curated", results)
	}
	if got := verifier.seen(); len(got) != 0 {
		t.Fatalf("verifier was called %v, want no verify call on a curated near-match", got)
	}
}

func TestVerifyPathShedsToUncheckedOnCapacity(t *testing.T) {
	t.Parallel()
	// A unit fans into more verify-path claims than the verify pool can run, with
	// no queue: the excess claims shed to unchecked (capacity), an honest terminal
	// state, while the pinned ones stay checking. The verifier blocks so the pool
	// stays saturated for the duration.
	unit := "claim one. claim two. claim three."
	c1, c2, c3 := "claim one fact.", "claim two fact.", "claim three fact."
	stream := pausingStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	// Every claim retrieves a wiki passage (no curated near-match), so all take the
	// verify path.
	mk := func(id string) []domain.SegmentMatch {
		return []domain.SegmentMatch{{Kind: domain.MatchKindEvidence, Claim: "passage", Similarity: 0.6, EvidenceID: id}}
	}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		c1: mk("evidence:1:0"), c2: mk("evidence:2:0"), c3: mk("evidence:3:0"),
	}}
	release := make(chan struct{})
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{}, release: release}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer:        fakeDecomposer{byText: map[string][]string{unit: {c1, c2, c3}}},
		Verifier:          verifier,
		VerifyConcurrency: 1,
		VerifyQueueDepth:  0,
		VerifyDeadline:    50 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	out, err := a.Run(ctx, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Collect until we have seen one unchecked-capacity result, then release the
	// blocked verifier and drain.
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

	if !sawUnchecked {
		t.Fatal("expected at least one claim shed to unchecked on capacity")
	}
}

func TestVerifyPathSkipsNonCheckableUnit(t *testing.T) {
	t.Parallel()
	// A unit the gate declines emits the legacy skip result per member and never
	// decomposes or emits a claims event, so a not_a_claim unit is reported the
	// same shape as on the old path.
	unit := "how are you?"
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{}
	verifier := &fakeVerifier{}

	vp, err := NewVerifyPath(VerifyPathConfig{
		Decomposer: fakeDecomposer{}, Matcher: matcher, Verifier: verifier,
		FastTau: 0.85, VerifyConcurrency: 1, FastDeadline: time.Second, VerifyDeadline: time.Second,
	})
	if err != nil {
		t.Fatalf("NewVerifyPath: %v", err)
	}
	a, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     stream,
		Matcher:    matcher,
		Prechecker: livePrechecker{skip: map[string]domain.SkipReason{unit: domain.SkipReasonNotAClaim}},
		Logger:     discardLogger(),
		Verify:     vp,
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	events := runVerifyPath(t, a)
	if firstOfKind(events, LiveEventClaims) != nil {
		t.Fatal("a non-checkable unit must not emit a claims event")
	}
	result := firstOfKind(events, LiveEventResult)
	if result == nil || result.SkipReason != domain.SkipReasonNotAClaim || result.ClaimID != "" {
		t.Fatalf("skip result = %+v, want legacy not_a_claim with no claim id", result)
	}
}

func TestVerifyPathCacheCollapsesRepeatedClaim(t *testing.T) {
	t.Parallel()
	// The same claim spoken twice within the cache TTL is verified once: the
	// second occurrence is served from the cache without a verify call.
	u1, u2 := "the treaty was signed in 1648.", "The treaty was signed in 1648."
	stream := &fakeSegmentStream{transcripts: finalize(
		domain.Segment{Start: time.Second, End: 2 * time.Second, Text: u1, Speaker: "A"},
		domain.Segment{Start: 3 * time.Second, End: 4 * time.Second, Text: u2, Speaker: "B"},
	)}
	mk := []domain.SegmentMatch{{Kind: domain.MatchKindEvidence, Claim: "Peace of Westphalia 1648", Similarity: 0.7, EvidenceID: "evidence:9:0"}}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{u1: mk, u2: mk}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		u1: {Verdict: "supports", Confidence: 0.8, Citations: []EvidenceCitation{{EvidenceID: "evidence:9:0", QuotedSpan: "1648"}}},
		u2: {Verdict: "supports", Confidence: 0.8, Citations: []EvidenceCitation{{EvidenceID: "evidence:9:0", QuotedSpan: "1648"}}},
	}}

	vp, err := NewVerifyPath(VerifyPathConfig{
		Decomposer: fakeDecomposer{}, Matcher: matcher, Verifier: verifier,
		FastTau: 0.85, VerifyConcurrency: 1, FastDeadline: time.Second, VerifyDeadline: time.Second, CacheTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewVerifyPath: %v", err)
	}
	// Concurrency 1 scores the two units sequentially, so the first populates the
	// cache before the second looks it up; this isolates the cache behavior from
	// the worker-pool scheduling, which a concurrent run would race.
	a, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream: stream, Matcher: matcher, Prechecker: allowAllPrechecker{}, Logger: discardLogger(), Concurrency: 1, Verify: vp,
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	_ = runVerifyPath(t, a)
	if got := verifier.seen(); len(got) != 1 {
		t.Fatalf("verifier called %d times %v, want 1 (second claim served from cache)", len(got), got)
	}
}

func TestNewVerifyPathValidates(t *testing.T) {
	t.Parallel()
	ok := VerifyPathConfig{Decomposer: fakeDecomposer{}, Matcher: liveMatcher{}, Verifier: &fakeVerifier{}, FastTau: 0.85, VerifyConcurrency: 1, FastDeadline: time.Second, VerifyDeadline: time.Second}
	tests := []struct {
		name   string
		mutate func(*VerifyPathConfig)
	}{
		{"nil decomposer", func(c *VerifyPathConfig) { c.Decomposer = nil }},
		{"nil matcher", func(c *VerifyPathConfig) { c.Matcher = nil }},
		{"nil verifier", func(c *VerifyPathConfig) { c.Verifier = nil }},
		{"zero concurrency", func(c *VerifyPathConfig) { c.VerifyConcurrency = 0 }},
		{"negative queue", func(c *VerifyPathConfig) { c.VerifyQueueDepth = -1 }},
		{"tau out of range", func(c *VerifyPathConfig) { c.FastTau = 1.5 }},
		{"non-positive fast deadline", func(c *VerifyPathConfig) { c.FastDeadline = 0 }},
		{"non-positive verify deadline", func(c *VerifyPathConfig) { c.VerifyDeadline = 0 }},
		{"negative cache ttl", func(c *VerifyPathConfig) { c.CacheTTL = -time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := ok
			tt.mutate(&cfg)
			if _, err := NewVerifyPath(cfg); err == nil {
				t.Fatalf("NewVerifyPath(%s) = nil error, want error", tt.name)
			}
		})
	}
}

func TestNormalizeClaim(t *testing.T) {
	t.Parallel()
	if got := normalizeClaim("  The   Earth\tis ROUND. "); got != "the earth is round." {
		t.Fatalf("normalizeClaim = %q", got)
	}
}

// runVerifyPath runs the analyzer over its stream and drains every event.
func runVerifyPath(t *testing.T, a *LiveAnalyzer) []LiveEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := a.Run(ctx, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return drainLiveEvents(t, out)
}

// firstOfKind returns the first event of kind, or nil.
func firstOfKind(events []LiveEvent, kind LiveEventKind) *LiveEvent {
	for i := range events {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	return nil
}

// resultsForClaim returns the result events for one claim id, in order.
func resultsForClaim(events []LiveEvent, claimID string) []LiveEvent {
	var out []LiveEvent
	for _, ev := range events {
		if ev.Kind == LiveEventResult && ev.ClaimID == claimID {
			out = append(out, ev)
		}
	}
	return out
}
