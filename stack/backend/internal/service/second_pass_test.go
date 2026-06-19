package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeReverifier returns the deeper re-judgment keyed by claim text and records
// every call, so a test can assert exactly which claims reached the second pass.
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

func testSecondPass(t *testing.T, lo, hi float64) *secondPass {
	t.Helper()
	sp, err := newSecondPass(SecondPassConfig{
		Reverifier: &fakeReverifier{},
		MidBandLo:  lo,
		MidBandHi:  hi,
		Deadline:   time.Second,
	})
	if err != nil {
		t.Fatalf("newSecondPass: %v", err)
	}
	return sp
}

func TestNewSecondPassValidation(t *testing.T) {
	t.Parallel()
	base := SecondPassConfig{Reverifier: &fakeReverifier{}, MidBandLo: 0.4, MidBandHi: 0.8, Deadline: time.Second}
	tests := []struct {
		name    string
		mutate  func(*SecondPassConfig)
		wantErr bool
	}{
		{"valid", func(*SecondPassConfig) {}, false},
		{"nil reverifier", func(c *SecondPassConfig) { c.Reverifier = nil }, true},
		{"low below range", func(c *SecondPassConfig) { c.MidBandLo = -0.1 }, true},
		{"high above range", func(c *SecondPassConfig) { c.MidBandHi = 1.1 }, true},
		{"inverted band", func(c *SecondPassConfig) { c.MidBandLo, c.MidBandHi = 0.8, 0.4 }, true},
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

func TestSecondPassQualifies(t *testing.T) {
	t.Parallel()
	sp := testSecondPass(t, 0.4, 0.8)
	tests := []struct {
		name         string
		verdict      *VerifiedVerdict
		passageCount int
		want         bool
	}{
		{
			name:         "evidence verdict in band with passages qualifies",
			verdict:      &VerifiedVerdict{Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.6},
			passageCount: 2,
			want:         true,
		},
		{
			name:         "evidence verdict at band low edge qualifies",
			verdict:      &VerifiedVerdict{Basis: BasisEvidence, Confidence: 0.4},
			passageCount: 1,
			want:         true,
		},
		{
			name:         "evidence verdict at band high edge qualifies",
			verdict:      &VerifiedVerdict{Basis: BasisEvidence, Confidence: 0.8},
			passageCount: 1,
			want:         true,
		},
		{
			name:         "confidence below band never qualifies",
			verdict:      &VerifiedVerdict{Basis: BasisEvidence, Confidence: 0.39},
			passageCount: 1,
			want:         false,
		},
		{
			name:         "confidence above band never qualifies",
			verdict:      &VerifiedVerdict{Basis: BasisEvidence, Confidence: 0.81},
			passageCount: 1,
			want:         false,
		},
		{
			name:         "knowledge basis never qualifies even in band",
			verdict:      &VerifiedVerdict{Basis: BasisKnowledge, Confidence: 0.6},
			passageCount: 3,
			want:         false,
		},
		{
			name:         "unverifiable knowledge verdict never qualifies",
			verdict:      &VerifiedVerdict{Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0.5},
			passageCount: 3,
			want:         false,
		},
		{
			name:         "evidence verdict with no passages never qualifies",
			verdict:      &VerifiedVerdict{Basis: BasisEvidence, Confidence: 0.6},
			passageCount: 0,
			want:         false,
		},
		{
			name:         "nil verdict never qualifies",
			verdict:      nil,
			passageCount: 2,
			want:         false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sp.qualifies(tc.verdict, tc.passageCount); got != tc.want {
				t.Errorf("qualifies = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSecondPassUpgrade(t *testing.T) {
	t.Parallel()
	sp := testSecondPass(t, 0.4, 0.8)
	matches := []domain.SegmentMatch{{Kind: domain.MatchKindEvidence, EvidenceID: "wiki:e:0", Claim: "evidence text"}}
	orig := &VerifiedVerdict{Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.6, Rationale: "fast"}

	t.Run("grounded re-judgment replaces the fast verdict", func(t *testing.T) {
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
		if got.Confidence != 0.95 || got.Basis != BasisEvidence {
			t.Errorf("upgraded = %+v, want evidence at 0.95", got)
		}
		if len(got.Citations) != 1 {
			t.Errorf("citations = %+v, want one surviving", got.Citations)
		}
	})

	t.Run("ungrounded knowledge re-judgment keeps the grounded fast verdict", func(t *testing.T) {
		t.Parallel()
		reasoned := ClaimVerdict{Verdict: VerdictCredible, Basis: BasisKnowledge, Confidence: 0.99, Rationale: "ungrounded"}
		got := sp.upgrade(orig, reasoned, matches)
		if got != orig {
			t.Errorf("upgrade = %+v, want the original grounded verdict kept", got)
		}
	})

	t.Run("evidence re-judgment whose citations resolve to nothing is capped", func(t *testing.T) {
		t.Parallel()
		// The reasoner claims evidence basis and high confidence but cites an id not
		// among the retrieved matches; verdictFromResult drops it, so the upgrade is no
		// longer grounded and must be demoted to a capped knowledge verdict - the cap
		// invariant enforced at the service layer too.
		reasoned := ClaimVerdict{
			Verdict:    VerdictCredible,
			Basis:      BasisEvidence,
			Confidence: 0.99,
			Citations:  []EvidenceCitation{{EvidenceID: "not-retrieved", QuotedSpan: "x"}},
			Rationale:  "confident but ungrounded",
		}
		got := sp.upgrade(orig, reasoned, matches)
		if got.Basis != BasisKnowledge {
			t.Errorf("basis = %q, want knowledge (no surviving citation)", got.Basis)
		}
		if got.Confidence > 0.6 {
			t.Errorf("confidence = %v, want capped at 0.6", got.Confidence)
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
		t.Fatalf("results = %d, want 2 (checking, verified) with no second pass", len(results))
	}
	if results[1].Verdict == nil || results[1].Verdict.Confidence != 0.6 {
		t.Fatalf("final verdict = %+v, want the fast verdict at 0.6 unchanged", results[1].Verdict)
	}
	if len(reverifier.seen()) != 0 {
		t.Fatalf("reverifier called %v times with the flag off, want 0", reverifier.seen())
	}
}

// TestVerifyPathSecondPassUpgradesInPlace proves the flag-on path: a grounded
// mid-confidence fast verdict is re-judged by the deeper reasoner and re-emitted in
// place at higher confidence, after the fast verdict has already emitted, for the
// same claim id.
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
		SecondPass: &SecondPassConfig{Reverifier: reverifier, MidBandLo: 0.4, MidBandHi: 0.8, Deadline: time.Second},
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
		SecondPass: &SecondPassConfig{Reverifier: reverifier, MidBandLo: 0.4, MidBandHi: 0.8, Deadline: time.Second},
	}
	a := verifyPathFixture(t, stream, matcher, vpCfg)

	_ = runVerifyPath(t, a)

	vp := a.verify
	cached, ok := vp.cacheGet(unit)
	if !ok {
		t.Fatal("claim not cached after the second pass")
	}
	if cached.verdict == nil || cached.verdict.Confidence != 0.95 {
		t.Fatalf("cached verdict = %+v, want the upgraded 0.95, not the stale fast verdict", cached.verdict)
	}
}

// TestVerifyPathSecondPassSkipsKnowledgeVerdict proves the gate: a knowledge-basis
// fast verdict (and the unverifiable no-evidence case) is never re-judged, so the
// reasoner is never invoked where it could only manufacture unsourced confidence.
func TestVerifyPathSecondPassSkipsKnowledgeVerdict(t *testing.T) {
	t.Parallel()
	unit := "most people slip into addiction gradually."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A"})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		unit: {{Kind: domain.MatchKindEvidence, Claim: "addiction onset varies", EvidenceID: "wiki:add:0", Similarity: 0.5}},
	}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		unit: {Verdict: VerdictCredible, Basis: BasisKnowledge, Confidence: 0.55, Rationale: "knowledge tiebreaker"},
	}}
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{}}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{},
		Verifier:   verifier,
		SecondPass: &SecondPassConfig{Reverifier: reverifier, MidBandLo: 0.4, MidBandHi: 0.8, Deadline: time.Second},
	})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil {
		t.Fatal("no claims event")
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (checking, verified) - no second pass on a knowledge verdict", len(results))
	}
	if seen := reverifier.seen(); len(seen) != 0 {
		t.Fatalf("reverifier called %v on a knowledge verdict, want never", seen)
	}
}
