package service

import (
	"errors"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// newBatchVerifyPath builds a VerifyPath with test defaults for the batch seam,
// wired to the supplied collaborators. Unlike the live fixture it returns the
// VerifyPath directly - the batch analyzer drives it, not a LiveAnalyzer.
func newBatchVerifyPath(t *testing.T, cfg VerifyPathConfig) *VerifyPath {
	t.Helper()
	if cfg.FastTau == 0 {
		cfg.FastTau = 0.85
	}
	if cfg.VerifyConcurrency == 0 {
		cfg.VerifyConcurrency = 2
	}
	if cfg.FastDeadline == 0 {
		cfg.FastDeadline = time.Second
	}
	if cfg.VerifyDeadline == 0 {
		cfg.VerifyDeadline = time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	vp, err := NewVerifyPath(cfg)
	if err != nil {
		t.Fatalf("NewVerifyPath: %v", err)
	}
	return vp
}

func TestAnalyzeTextGateSkip(t *testing.T) {
	t.Parallel()
	text := "bonjour tout le monde"
	gate := livePrechecker{skip: map[string]domain.SkipReason{text: domain.SkipReasonNotAClaim}}
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer: fakeDecomposer{}, Matcher: liveMatcher{}, Verifier: &fakeVerifier{},
	})

	res, err := vp.AnalyzeText(t.Context(), gate, text, "s0")
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if res.Checkable {
		t.Errorf("gated text reported checkable: %+v", res)
	}
	if res.SkipReason != domain.SkipReasonNotAClaim {
		t.Errorf("skip reason = %q, want not_a_claim", res.SkipReason)
	}
	if len(res.Claims) != 0 {
		t.Errorf("gated text produced claims: %+v", res.Claims)
	}
}

func TestAnalyzeTextGateError(t *testing.T) {
	t.Parallel()
	text := "une phrase"
	gate := livePrechecker{err: map[string]error{text: errors.New("gate down")}}
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer: fakeDecomposer{}, Matcher: liveMatcher{}, Verifier: &fakeVerifier{},
	})

	if _, err := vp.AnalyzeText(t.Context(), gate, text, "s0"); err == nil {
		t.Fatal("gate failure was swallowed")
	}
}

func TestAnalyzeTextNotAClaim(t *testing.T) {
	t.Parallel()
	// A checkable unit whose decomposition drops every fragment carries no
	// verifiable claim: a not_a_claim skip, not a fan-out.
	text := "peut-etre demain"
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{text: {}}},
		Matcher:    liveMatcher{}, Verifier: &fakeVerifier{},
	})

	res, err := vp.AnalyzeText(t.Context(), allowAllPrechecker{}, text, "s0")
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if !res.Checkable {
		t.Errorf("checkable-but-empty unit reported not checkable")
	}
	if res.SkipReason != domain.SkipReasonNotAClaim {
		t.Errorf("skip reason = %q, want not_a_claim", res.SkipReason)
	}
	if len(res.Claims) != 0 {
		t.Errorf("empty decomposition produced claims: %+v", res.Claims)
	}
}

func TestAnalyzeTextCuratedFastBorrow(t *testing.T) {
	t.Parallel()
	// A high-similarity curated claim match is borrowed with no verifier call.
	text := "la terre est ronde"
	match := []domain.SegmentMatch{{
		Kind: domain.MatchKindClaim, Claim: "la Terre est un spheroide", Verdict: domain.VerdictCorroborates,
		Sources: []domain.Source{{Title: "NASA", URL: "https://nasa.example"}}, Similarity: 0.95,
	}}
	verifier := &fakeVerifier{}
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{text: {text}}},
		Matcher:    liveMatcher{matches: map[string][]domain.SegmentMatch{text: match}},
		Verifier:   verifier,
	})

	res, err := vp.AnalyzeText(t.Context(), allowAllPrechecker{}, text, "s0")
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if len(res.Claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(res.Claims))
	}
	c := res.Claims[0]
	if c.Status != ClaimStatusVerified || c.Source != SourceCurated {
		t.Errorf("claim = status %q source %q, want verified/curated", c.Status, c.Source)
	}
	if c.Claim.ClaimID != "s0-0" {
		t.Errorf("claim id = %q, want s0-0", c.Claim.ClaimID)
	}
	if c.Verdict == nil || c.Verdict.Verdict != VerdictCredible || c.Verdict.Basis != BasisEvidence {
		t.Errorf("verdict = %+v, want credible/evidence", c.Verdict)
	}
	if len(verifier.seen()) != 0 {
		t.Errorf("curated borrow still called the verifier: %v", verifier.seen())
	}
}

func TestAnalyzeTextVerifies(t *testing.T) {
	t.Parallel()
	// Non-curated evidence (below the fast tau) routes to the verifier.
	text := "le budget a double"
	match := []domain.SegmentMatch{{
		Kind: domain.MatchKindClaim, Claim: "budget +40%", Verdict: domain.VerdictContradicts,
		Similarity: 0.5, EvidenceID: "ev-1",
	}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		text: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.8, Rationale: "les chiffres contredisent", Citations: []EvidenceCitation{{EvidenceID: "ev-1", QuotedSpan: "budget +40%"}}},
	}}
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{text: {text}}},
		Matcher:    liveMatcher{matches: map[string][]domain.SegmentMatch{text: match}},
		Verifier:   verifier,
	})

	res, err := vp.AnalyzeText(t.Context(), allowAllPrechecker{}, text, "s0")
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if len(res.Claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(res.Claims))
	}
	c := res.Claims[0]
	if c.Status != ClaimStatusVerified || c.Source != SourceVerified {
		t.Errorf("claim = status %q source %q, want verified/verified", c.Status, c.Source)
	}
	if c.Verdict == nil || c.Verdict.Verdict != VerdictDisputed || c.Verdict.Confidence != 0.8 {
		t.Errorf("verdict = %+v, want disputed 0.8", c.Verdict)
	}
	if len(c.Verdict.Citations) != 1 || c.Verdict.Citations[0].EvidenceID != "ev-1" {
		t.Errorf("citations = %+v, want the cited match", c.Verdict.Citations)
	}
	if len(verifier.seen()) != 1 {
		t.Errorf("verifier calls = %v, want one", verifier.seen())
	}
}

func TestAnalyzeTextNoEvidence(t *testing.T) {
	t.Parallel()
	// No retrieved evidence: the honest unverifiable/knowledge outcome, no
	// verifier call.
	text := "une affirmation obscure"
	verifier := &fakeVerifier{}
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{text: {text}}},
		Matcher:    liveMatcher{}, Verifier: verifier,
	})

	res, err := vp.AnalyzeText(t.Context(), allowAllPrechecker{}, text, "s0")
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if len(res.Claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(res.Claims))
	}
	c := res.Claims[0]
	if c.Status != ClaimStatusVerified || c.Verdict == nil || c.Verdict.Verdict != VerdictUnverifiable || c.Verdict.Basis != BasisKnowledge {
		t.Errorf("claim = %+v / %+v, want verified unverifiable/knowledge", c, c.Verdict)
	}
	if len(verifier.seen()) != 0 {
		t.Errorf("no-evidence claim still called the verifier: %v", verifier.seen())
	}
}

func TestAnalyzeTextVerifierError(t *testing.T) {
	t.Parallel()
	text := "une phrase qui casse le verifier"
	match := []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Similarity: 0.5, EvidenceID: "ev-1"}}
	verifier := &fakeVerifier{err: map[string]error{text: errors.New("verifier down")}}
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{text: {text}}},
		Matcher:    liveMatcher{matches: map[string][]domain.SegmentMatch{text: match}},
		Verifier:   verifier,
	})

	res, err := vp.AnalyzeText(t.Context(), allowAllPrechecker{}, text, "s0")
	if err != nil {
		t.Fatalf("AnalyzeText returned a fatal error for a per-claim failure: %v", err)
	}
	if len(res.Claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(res.Claims))
	}
	if c := res.Claims[0]; c.Status != ClaimStatusError || c.Verdict != nil {
		t.Errorf("claim = %+v, want error with no verdict", c)
	}
}

// TestAnalyzeTextNeverSheds proves the batch path applies backpressure instead
// of shedding: with the verify pool at concurrency 1 and a sentence carrying
// three verify-bound claims, all three still resolve to verified (the live path
// would shed to unchecked). The verifier is released immediately, so the claims
// queue on the pool semaphore and drain in turn rather than being dropped.
func TestAnalyzeTextNeverSheds(t *testing.T) {
	t.Parallel()
	text := "trois affirmations"
	c1, c2, c3 := "premiere", "deuxieme", "troisieme"
	match := []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Similarity: 0.5, EvidenceID: "ev-1"}}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		c1: {Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.7},
		c2: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.7},
		c3: {Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.7},
	}}
	vp := newBatchVerifyPath(t, VerifyPathConfig{
		Decomposer:        fakeDecomposer{byText: map[string][]string{text: {c1, c2, c3}}},
		Matcher:           liveMatcher{matches: map[string][]domain.SegmentMatch{c1: match, c2: match, c3: match}},
		Verifier:          verifier,
		VerifyConcurrency: 1,
		VerifyQueueDepth:  0,
	})

	res, err := vp.AnalyzeText(t.Context(), allowAllPrechecker{}, text, "s0")
	if err != nil {
		t.Fatalf("AnalyzeText: %v", err)
	}
	if len(res.Claims) != 3 {
		t.Fatalf("claims = %d, want 3", len(res.Claims))
	}
	for i, c := range res.Claims {
		if c.Status != ClaimStatusVerified {
			t.Errorf("claim %d status = %q, want verified (never shed to unchecked)", i, c.Status)
		}
		if c.Status == ClaimStatusUnchecked {
			t.Errorf("claim %d was shed to unchecked - batch must not shed", i)
		}
	}
	if len(verifier.seen()) != 3 {
		t.Errorf("verifier calls = %d, want all 3 (none shed)", len(verifier.seen()))
	}
}
