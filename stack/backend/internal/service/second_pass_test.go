package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeReverifier returns the deeper re-judgment keyed by claim text and records
// every call, so a test can assert exactly which claims reached the terminal gate.
type fakeReverifier struct {
	byClaim map[string]ClaimVerdict
	err     map[string]error

	mu    sync.Mutex
	calls []string
}

func (r *fakeReverifier) Reverify(_ context.Context, claim string, _ []EvidencePassage) (ClaimVerdict, error) {
	r.mu.Lock()
	r.calls = append(r.calls, claim)
	r.mu.Unlock()
	if err := r.err[claim]; err != nil {
		return ClaimVerdict{}, err
	}
	return r.byClaim[claim], nil
}

func (r *fakeReverifier) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func testSecondPass(t *testing.T, triggerBelow, minConfidence float64) *secondPass {
	t.Helper()
	sp, err := newSecondPass(SecondPassConfig{
		Reverifier:    &fakeReverifier{},
		TriggerBelow:  triggerBelow,
		MinConfidence: minConfidence,
		Deadline:      time.Second,
	})
	if err != nil {
		t.Fatalf("newSecondPass: %v", err)
	}
	return sp
}

func TestNewSecondPassValidation(t *testing.T) {
	t.Parallel()
	base := SecondPassConfig{Reverifier: &fakeReverifier{}, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second}
	tests := []struct {
		name    string
		mutate  func(*SecondPassConfig)
		wantErr bool
	}{
		{"valid", func(*SecondPassConfig) {}, false},
		{"nil reverifier", func(c *SecondPassConfig) { c.Reverifier = nil }, true},
		{"trigger below range", func(c *SecondPassConfig) { c.TriggerBelow = -0.1 }, true},
		{"trigger above range", func(c *SecondPassConfig) { c.TriggerBelow = 1.1 }, true},
		{"min confidence below range", func(c *SecondPassConfig) { c.MinConfidence = -0.1 }, true},
		{"min confidence above range", func(c *SecondPassConfig) { c.MinConfidence = 1.1 }, true},
		{"non-positive deadline", func(c *SecondPassConfig) { c.Deadline = 0 }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.mutate(&cfg)
			_, err := newSecondPass(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestSecondPassWeak covers the inverted terminal-gate trigger: the gate fires on a
// weak verdict (unverifiable, or confidence below the trigger floor) regardless of
// basis, and only when there are passages to re-read.
func TestSecondPassWeak(t *testing.T) {
	t.Parallel()
	sp := testSecondPass(t, 0.8, 0.9)
	tests := []struct {
		name         string
		verdict      *VerifiedVerdict
		passageCount int
		want         bool
	}{
		{
			name:         "unverifiable with passages is weak even at high confidence",
			verdict:      &VerifiedVerdict{Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0.99},
			passageCount: 2,
			want:         true,
		},
		{
			name:         "confidence below the trigger floor is weak",
			verdict:      &VerifiedVerdict{Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.5},
			passageCount: 1,
			want:         true,
		},
		{
			name:         "knowledge-basis low confidence is weak (basis no longer gates)",
			verdict:      &VerifiedVerdict{Verdict: VerdictCredible, Basis: BasisKnowledge, Confidence: 0.5},
			passageCount: 3,
			want:         true,
		},
		{
			name:         "confidence at the trigger floor is not weak",
			verdict:      &VerifiedVerdict{Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.8},
			passageCount: 1,
			want:         false,
		},
		{
			name:         "confidence above the trigger floor is not weak",
			verdict:      &VerifiedVerdict{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.85},
			passageCount: 1,
			want:         false,
		},
		{
			name:         "weak but no passages never fires (nothing to ground)",
			verdict:      &VerifiedVerdict{Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.5},
			passageCount: 0,
			want:         false,
		},
		{
			name:         "unverifiable with no passages never fires",
			verdict:      &VerifiedVerdict{Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0},
			passageCount: 0,
			want:         false,
		},
		{
			name:         "nil verdict never fires",
			verdict:      nil,
			passageCount: 2,
			want:         false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sp.weak(tc.verdict, tc.passageCount); got != tc.want {
				t.Errorf("weak = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSecondPassAccept covers the acceptance rule: a re-judgment is adopted only when
// it is grounded (evidence basis with a surviving citation) AND reaches the confidence
// floor.
func TestSecondPassAccept(t *testing.T) {
	t.Parallel()
	sp := testSecondPass(t, 0.8, 0.9)
	cite := []EvidenceCitation{{EvidenceID: "wiki:e:0", QuotedSpan: "x"}}
	tests := []struct {
		name     string
		reasoned ClaimVerdict
		want     bool
	}{
		{"grounded above the floor is accepted", ClaimVerdict{Basis: BasisEvidence, Confidence: 0.95, Citations: cite}, true},
		{"grounded at the floor is accepted", ClaimVerdict{Basis: BasisEvidence, Confidence: 0.9, Citations: cite}, true},
		{"grounded below the floor is rejected", ClaimVerdict{Basis: BasisEvidence, Confidence: 0.89, Citations: cite}, false},
		{"evidence basis with no citation is rejected", ClaimVerdict{Basis: BasisEvidence, Confidence: 0.99}, false},
		{"knowledge basis is rejected however confident", ClaimVerdict{Basis: BasisKnowledge, Confidence: 0.99, Citations: cite}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sp.accept(tc.reasoned); got != tc.want {
				t.Errorf("accept = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSecondPassUpgrade covers the fold: an accepted grounded, high-confidence
// re-judgment replaces the weak verdict; every rejection keeps the prior verdict
// (unverifiable stays unverifiable, low-confidence-but-valid is retained).
func TestSecondPassUpgrade(t *testing.T) {
	t.Parallel()
	sp := testSecondPass(t, 0.8, 0.9)
	matches := []domain.SegmentMatch{{Kind: domain.MatchKindEvidence, EvidenceID: "wiki:e:0", Claim: "evidence text"}}
	// The prior verdict the gate fired on: a weak, unverifiable pipeline verdict.
	orig := &VerifiedVerdict{Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0.3, Rationale: "fast"}

	t.Run("accepted re-judgment replaces the weak verdict", func(t *testing.T) {
		t.Parallel()
		reasoned := ClaimVerdict{
			Verdict:    VerdictDisputed,
			Basis:      BasisEvidence,
			Confidence: 0.95,
			Citations:  []EvidenceCitation{{EvidenceID: "wiki:e:0", QuotedSpan: "evidence text"}},
			Rationale:  "deeper",
		}
		got := sp.upgrade(orig, reasoned, matches)
		if got == orig {
			t.Fatal("expected an upgraded verdict, got the original")
		}
		if got.Verdict != VerdictDisputed || got.Confidence != 0.95 || got.Basis != BasisEvidence {
			t.Errorf("upgraded = %+v, want disputed evidence at 0.95", got)
		}
		if len(got.Citations) != 1 {
			t.Errorf("citations = %+v, want one surviving", got.Citations)
		}
	})

	t.Run("ungrounded knowledge re-judgment keeps the prior verdict", func(t *testing.T) {
		t.Parallel()
		reasoned := ClaimVerdict{Verdict: VerdictCredible, Basis: BasisKnowledge, Confidence: 0.99, Rationale: "ungrounded"}
		if got := sp.upgrade(orig, reasoned, matches); got != orig {
			t.Errorf("upgrade = %+v, want the prior unverifiable verdict kept", got)
		}
	})

	t.Run("grounded but below the confidence floor keeps the prior verdict", func(t *testing.T) {
		t.Parallel()
		reasoned := ClaimVerdict{
			Verdict:    VerdictDisputed,
			Basis:      BasisEvidence,
			Confidence: 0.85,
			Citations:  []EvidenceCitation{{EvidenceID: "wiki:e:0", QuotedSpan: "evidence text"}},
		}
		if got := sp.upgrade(orig, reasoned, matches); got != orig {
			t.Errorf("upgrade = %+v, want the prior verdict kept (below floor)", got)
		}
	})

	t.Run("evidence re-judgment whose citations resolve to nothing keeps the prior", func(t *testing.T) {
		t.Parallel()
		// The reasoner claims evidence basis and high confidence but cites an id not
		// among the retrieved matches; verdictFromResult drops it, so the upgrade is no
		// longer grounded and must not be adopted - the prior weak verdict stands rather
		// than an unsourced high-confidence claim.
		reasoned := ClaimVerdict{
			Verdict:    VerdictCredible,
			Basis:      BasisEvidence,
			Confidence: 0.99,
			Citations:  []EvidenceCitation{{EvidenceID: "not-retrieved", QuotedSpan: "x"}},
			Rationale:  "confident but ungrounded",
		}
		if got := sp.upgrade(orig, reasoned, matches); got != orig {
			t.Errorf("upgrade = %+v, want the prior verdict kept (no surviving citation)", got)
		}
	})
}

// TestVerifyPathSecondPassOffIsNoOp proves the flag-off path is byte-for-byte the
// legacy single-pass verify behavior: with no SecondPass config the reverifier is
// never built and never called, and the verified claim emits exactly one
// checking+verified pair with the fast verdict.
func TestVerifyPathSecondPassOffIsNoOp(t *testing.T) {
	t.Parallel()
	unit := "the moon is made of rock."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		unit: {{Kind: domain.MatchKindEvidence, Claim: "the moon is rock", EvidenceID: "wiki:moon:0", Similarity: 0.5}},
	}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.6, Citations: []EvidenceCitation{{EvidenceID: "wiki:moon:0", QuotedSpan: "rock"}}, Rationale: "fast"},
	}}
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.95, Citations: []EvidenceCitation{{EvidenceID: "wiki:moon:0", QuotedSpan: "rock"}}},
	}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Verifier:   verifier,
		// SecondPass intentionally nil: the legacy single-pass path.
	})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil {
		t.Fatal("no claims event")
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (checking, verified) with no terminal gate", len(results))
	}
	if results[1].Verdict == nil || results[1].Verdict.Confidence != 0.6 {
		t.Fatalf("final verdict = %+v, want the fast verdict at 0.6 unchanged", results[1].Verdict)
	}
	if len(reverifier.seen()) != 0 {
		t.Fatalf("reverifier called %v times with the flag off, want 0", reverifier.seen())
	}
}

// TestVerifyPathSecondPassUpgradesInPlace proves the flag-on path: a weak fast verdict
// is re-judged by the deeper reasoner and, when the re-judgment is grounded and
// high-confidence, re-emitted in place at higher confidence, after the fast verdict has
// already emitted, for the same claim id.
func TestVerifyPathSecondPassUpgradesInPlace(t *testing.T) {
	t.Parallel()
	unit := "the moon is made of rock."
	match := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "the moon is rock", EvidenceID: "wiki:moon:0", Similarity: 0.5}
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{unit: {match}}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.55, Citations: []EvidenceCitation{{EvidenceID: "wiki:moon:0", QuotedSpan: "rock"}}, Rationale: "fast"},
	}}
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.95, Citations: []EvidenceCitation{{EvidenceID: "wiki:moon:0", QuotedSpan: "rock"}}, Rationale: "deeper"},
	}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Verifier:   verifier,
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil {
		t.Fatal("no claims event")
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	// checking, fast verified, then the upgraded verified re-emit.
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3 (checking, fast verified, upgraded verified)", len(results))
	}
	if results[1].Verdict == nil || results[1].Verdict.Confidence != 0.55 {
		t.Fatalf("fast verdict emitted first = %+v, want 0.55", results[1].Verdict)
	}
	if results[2].ClaimStatus != ClaimStatusVerified || results[2].Source != SourceVerified {
		t.Fatalf("upgrade result = status %q source %q, want verified/verified", results[2].ClaimStatus, results[2].Source)
	}
	if results[2].Verdict == nil || results[2].Verdict.Confidence != 0.95 {
		t.Fatalf("upgraded verdict = %+v, want 0.95", results[2].Verdict)
	}
	if results[2].Verdict.Rationale != "deeper" {
		t.Fatalf("upgraded rationale = %q, want the deeper rationale", results[2].Verdict.Rationale)
	}
	if seen := reverifier.seen(); len(seen) != 1 {
		t.Fatalf("reverifier calls = %v, want exactly one", seen)
	}
}

// TestVerifyPathSecondPassRecachesUpgrade proves an upgraded verdict overwrites the
// fast verdict in the short-TTL cache, so a repeat of the same claim within the
// window replays the deeper verdict rather than the stale fast one.
func TestVerifyPathSecondPassRecachesUpgrade(t *testing.T) {
	t.Parallel()
	unit := "the moon is made of rock."
	match := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "the moon is rock", EvidenceID: "wiki:moon:0", Similarity: 0.5}
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{unit: {match}}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.55, Citations: []EvidenceCitation{{EvidenceID: "wiki:moon:0", QuotedSpan: "rock"}}},
	}}
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.95, Citations: []EvidenceCitation{{EvidenceID: "wiki:moon:0", QuotedSpan: "rock"}}},
	}}

	vpCfg := VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Matcher:    matcher,
		Verifier:   verifier,
		FastTau:    0.85,
		CacheTTL:   time.Minute,
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	}
	a := verifyPathFixture(t, stream, matcher, vpCfg)

	_ = runVerifyPath(t, a)

	vp := a.verify
	cached, ok := vp.cacheGet(unit)
	if !ok {
		t.Fatal("claim not cached after the terminal gate")
	}
	if cached.verdict == nil || cached.verdict.Confidence != 0.95 {
		t.Fatalf("cached verdict = %+v, want the upgraded 0.95, not the stale fast verdict", cached.verdict)
	}
}

// TestVerifyPathTerminalGateMovesTallyOnUpgrade proves the speaker tally moves with a
// weak-verdict upgrade rather than diverging from the displayed verdict: the fast pass
// counts an unverifiable claim, then the gate upgrades it to disputed and re-tallies it
// (unverifiable 1 -> 0, disputed 0 -> 1) - no double-count, and the aggregate matches
// the UI.
func TestVerifyPathTerminalGateMovesTallyOnUpgrade(t *testing.T) {
	t.Parallel()
	unit := "the moon is made of rock."
	match := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "the moon is rock", EvidenceID: "wiki:moon:0", Similarity: 0.5}
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{unit: {match}}}
	// Fast pass is unsure: unverifiable, but there IS evidence to re-read, so the gate fires.
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0.3},
	}}
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.95, Citations: []EvidenceCitation{{EvidenceID: "wiki:moon:0", QuotedSpan: "rock"}}},
	}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Verifier:   verifier,
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	})

	events := runVerifyPath(t, a)
	tallies := speakerTallies(events)
	if len(tallies) < 2 {
		t.Fatalf("speaker tally events = %d, want at least 2 (fast, then the corrected re-tally)", len(tallies))
	}
	final := tallies[len(tallies)-1]
	if final.Disputed != 1 || final.Unverifiable != 0 {
		t.Fatalf("final tally = disputed %d unverifiable %d, want the claim moved to disputed (1/0)", final.Disputed, final.Unverifiable)
	}
}

// TestVerifyPathTerminalGateStrongVerdictSkipped proves a strong fast verdict
// (confidence at or above the trigger floor, not unverifiable) is never escalated, so
// the reasoner is never invoked and the fast verdict stands as the only verified frame.
func TestVerifyPathTerminalGateStrongVerdictSkipped(t *testing.T) {
	t.Parallel()
	unit := "the earth orbits the sun."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		unit: {{Kind: domain.MatchKindEvidence, Claim: "earth orbits sun", EvidenceID: "wiki:sun:0", Similarity: 0.5}},
	}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.92, Citations: []EvidenceCitation{{EvidenceID: "wiki:sun:0", QuotedSpan: "orbit"}}, Rationale: "fast"},
	}}
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Verifier:   verifier,
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil {
		t.Fatal("no claims event")
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (checking, verified) - a strong verdict skips the gate", len(results))
	}
	if seen := reverifier.seen(); len(seen) != 0 {
		t.Fatalf("reverifier called %v on a strong verdict, want never", seen)
	}
}

// TestVerifyPathTerminalGateKeepsPriorWhenUngrounded proves the precedence rule at the
// path level: a weak verdict DOES fire the gate (the reasoner is called), but when the
// re-judgment cannot ground, the prior verdict stands and no upgrade frame is emitted.
func TestVerifyPathTerminalGateKeepsPriorWhenUngrounded(t *testing.T) {
	t.Parallel()
	unit := "most people slip into addiction gradually."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		unit: {{Kind: domain.MatchKindEvidence, Claim: "addiction onset varies", EvidenceID: "wiki:add:0", Similarity: 0.5}},
	}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0.3, Rationale: "cannot check"},
	}}
	// The deeper model also cannot ground it: knowledge basis, no citation.
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictCredible, Basis: BasisKnowledge, Confidence: 0.99, Rationale: "guess"},
	}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Verifier:   verifier,
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil {
		t.Fatal("no claims event")
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (checking, verified) - an ungrounded re-judgment keeps the prior", len(results))
	}
	if results[1].Verdict == nil || results[1].Verdict.Verdict != VerdictUnverifiable {
		t.Fatalf("final verdict = %+v, want the prior unverifiable verdict kept", results[1].Verdict)
	}
	if seen := reverifier.seen(); len(seen) != 1 {
		t.Fatalf("reverifier calls = %v, want exactly one (the weak verdict fired the gate)", seen)
	}
}
