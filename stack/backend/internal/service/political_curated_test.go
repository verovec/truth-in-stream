package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// stubPoliticalClaimSearcher returns a fixed result (or error) for the curated
// political fast-path, so the borrow logic is exercised with no store.
type stubPoliticalClaimSearcher struct {
	matches []domain.PoliticalClaimMatch
	err     error
	calls   int
}

func (s *stubPoliticalClaimSearcher) SearchPoliticalClaims(_ context.Context, _ []float32, _ int, _ int) ([]domain.PoliticalClaimMatch, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.matches, nil
}

// curatedVerifyPath builds a VerifyPath whose only wired political collaborator is
// the curated store at the default 0.85 borrow bar, enough to exercise
// politicalFastMatch directly.
func curatedVerifyPath(store PoliticalClaimSearcher) *VerifyPath {
	return &VerifyPath{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		pol:    &PoliticalConfig{CuratedStore: store, CuratedTau: 0.85},
	}
}

func TestPoliticalFastMatchBorrowsTwoAxisVerdict(t *testing.T) {
	store := &stubPoliticalClaimSearcher{matches: []domain.PoliticalClaimMatch{{
		ID:             "imm-1",
		Text:           "500 000 immigrés entrent chaque année dont seuls 10% travaillent",
		LiteralVerdict: domain.LiteralInaccurate,
		Flags:          []domain.ManipulationFlag{domain.FlagCherryPicked, domain.FlagMissingContext},
		SourceName:     "INSEE",
		SourceURL:      "https://www.insee.fr/fr/statistiques/8998082",
		QuotedSpan:     "375 000 entrees en 2022 ; 62,5% en emploi.",
		Distance:       0,
	}}}
	vp := curatedVerifyPath(store)

	verdict, ok := vp.politicalFastMatch(context.Background(), make([]float32, domain.EmbeddingDim))
	if !ok {
		t.Fatal("expected a curated borrow, got ok=false")
	}
	if verdict.Literal != LiteralInaccurate {
		t.Errorf("Literal = %q, want inaccurate", verdict.Literal)
	}
	if len(verdict.Flags) != 2 || verdict.Flags[0] != string(domain.FlagCherryPicked) {
		t.Errorf("Flags = %v, want [cherry-picked missing-context]", verdict.Flags)
	}
	if verdict.Verdict != VerdictDisputed {
		t.Errorf("Verdict = %q, want disputed (inaccurate -> disputed)", verdict.Verdict)
	}
	if verdict.Basis != BasisEvidence {
		t.Errorf("Basis = %q, want evidence", verdict.Basis)
	}
	if verdict.Confidence != 1 {
		t.Errorf("Confidence = %v, want 1 (distance 0)", verdict.Confidence)
	}
	if len(verdict.Citations) != 1 {
		t.Fatalf("Citations = %d, want 1", len(verdict.Citations))
	}
	cit := verdict.Citations[0]
	if cit.Kind != domain.MatchKindClaim {
		t.Errorf("citation kind = %q, want claim", cit.Kind)
	}
	if len(cit.Sources) != 1 || cit.Sources[0].URL != "https://www.insee.fr/fr/statistiques/8998082" {
		t.Errorf("citation sources = %v, want the INSEE url", cit.Sources)
	}
	if cit.EvidenceID != "imm-1" {
		t.Errorf("citation evidence id = %q, want imm-1", cit.EvidenceID)
	}
}

func TestPoliticalFastMatchBelowTauMisses(t *testing.T) {
	store := &stubPoliticalClaimSearcher{matches: []domain.PoliticalClaimMatch{{
		ID:             "imm-1",
		LiteralVerdict: domain.LiteralInaccurate,
		Distance:       0.4, // similarity 0.6 < 0.85
	}}}
	vp := curatedVerifyPath(store)

	if _, ok := vp.politicalFastMatch(context.Background(), make([]float32, domain.EmbeddingDim)); ok {
		t.Error("expected miss below tau, got a borrow")
	}
}

func TestPoliticalFastMatchNoStoreMisses(t *testing.T) {
	vp := &VerifyPath{pol: &PoliticalConfig{CuratedStore: nil, CuratedTau: 0.85}}
	if _, ok := vp.politicalFastMatch(context.Background(), make([]float32, domain.EmbeddingDim)); ok {
		t.Error("expected miss with no curated store, got a borrow")
	}
}

func TestPoliticalFastMatchEmptyEmbeddingMisses(t *testing.T) {
	store := &stubPoliticalClaimSearcher{matches: []domain.PoliticalClaimMatch{{ID: "imm-1", LiteralVerdict: domain.LiteralInaccurate, Distance: 0}}}
	vp := curatedVerifyPath(store)
	if _, ok := vp.politicalFastMatch(context.Background(), nil); ok {
		t.Error("expected miss with empty embedding, got a borrow")
	}
	if store.calls != 0 {
		t.Errorf("store searched %d times on empty embedding, want 0", store.calls)
	}
}

func TestPoliticalFastMatchNoHitsMisses(t *testing.T) {
	store := &stubPoliticalClaimSearcher{matches: nil}
	vp := curatedVerifyPath(store)
	if _, ok := vp.politicalFastMatch(context.Background(), make([]float32, domain.EmbeddingDim)); ok {
		t.Error("expected miss with no hits, got a borrow")
	}
}

func TestPoliticalFastMatchSearchErrorFallsThrough(t *testing.T) {
	store := &stubPoliticalClaimSearcher{err: errors.New("boom")}
	vp := curatedVerifyPath(store)
	if _, ok := vp.politicalFastMatch(context.Background(), make([]float32, domain.EmbeddingDim)); ok {
		t.Error("expected miss (fall-through) on search error, got a borrow")
	}
}

// TestImmigrationClaimResolvesViaCuratedPathNotUnverifiable is the card's
// acceptance e2e: with the political path on and the curated immigration claim
// seeded, the motivating statement resolves through the curated fast-path to a
// grounded, cited two-axis verdict (literal inaccurate + a manipulation flag + the
// real source URL), tagged curated - never the "Invérifiable" no-evidence outcome.
// The classify/route/verify collaborators are asserted untouched, proving the
// curated borrow short-circuits the LLM stages entirely.
func TestImmigrationClaimResolvesViaCuratedPathNotUnverifiable(t *testing.T) {
	t.Parallel()
	unit := "500 000 immigrés entrent chaque année dont seuls 10% travaillent"
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A",
	})}
	// No legacy curated hit: the legacy matcher returns nothing, so resolution can
	// only come from the political curated store (or fall through to route+verify,
	// which here would return unverifiable). It does return a query embedding, which
	// the curated fast-path reuses to search the political_claims corpus.
	matcher := liveMatcher{
		matches:   map[string][]domain.SegmentMatch{},
		embedding: map[string][]float32{unit: make([]float32, domain.EmbeddingDim)},
	}
	curated := &stubPoliticalClaimSearcher{matches: []domain.PoliticalClaimMatch{{
		ID:             "imm-500k-entrent-10pct-travaillent",
		Text:           unit,
		LiteralVerdict: domain.LiteralInaccurate,
		Flags:          []domain.ManipulationFlag{domain.FlagCherryPicked, domain.FlagMissingContext},
		SourceName:     "INSEE - Flux migratoires",
		SourceURL:      "https://www.insee.fr/fr/statistiques/8998082",
		QuotedSpan:     "375 000 entrees en 2022 ; 62,5% en emploi (2022-2023).",
		Distance:       0, // deterministic exact-text match -> similarity 1
	}}}
	classifier := fakeClassifier{}
	router := &fakeRouterRetriever{}     // must stay untouched on a curated borrow
	verifier := &fakePoliticalVerifier{} // must stay untouched on a curated borrow

	a := politicalFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {unit}}},
		Verifier:   &fakeVerifier{},
	}, PoliticalConfig{
		Classifier:   classifier,
		Retriever:    router,
		Verifier:     verifier,
		CuratedStore: curated,
		CuratedTau:   0.85,
	})

	events := runVerifyPath(t, a)
	claimsEv := firstOfKind(events, LiveEventClaims)
	if claimsEv == nil || len(claimsEv.Claims) != 1 {
		t.Fatalf("claims event = %+v, want one claim", claimsEv)
	}
	results := resultsForClaim(events, claimsEv.Claims[0].ClaimID)
	if len(results) != 1 {
		t.Fatalf("results = %+v, want a single curated result (no checking frame)", results)
	}
	got := results[0]
	if got.ClaimStatus != ClaimStatusVerified || got.Source != SourceCurated {
		t.Fatalf("terminal = status %q source %q, want verified/curated", got.ClaimStatus, got.Source)
	}
	if got.Verdict == nil {
		t.Fatal("curated result carries no verdict")
	}
	if got.Verdict.Literal != LiteralInaccurate {
		t.Fatalf("literal = %q, want inaccurate (not unverifiable)", got.Verdict.Literal)
	}
	if got.Verdict.Verdict == VerdictUnverifiable {
		t.Fatal("verdict resolved to unverifiable - the curated path did not ground it")
	}
	if len(got.Verdict.Flags) == 0 {
		t.Fatal("verdict carries no manipulation flag - the two-axis framing was lost")
	}
	if len(got.Verdict.Citations) != 1 || len(got.Verdict.Citations[0].Sources) != 1 {
		t.Fatalf("citations = %+v, want one with one source", got.Verdict.Citations)
	}
	if url := got.Verdict.Citations[0].Sources[0].URL; url != "https://www.insee.fr/fr/statistiques/8998082" {
		t.Fatalf("cited source url = %q, want the real INSEE url", url)
	}
	if seen := verifier.seen(); len(seen) != 0 {
		t.Fatalf("two-axis verifier called %v, want none on a curated borrow (no LLM)", seen)
	}
	if routed := router.routed(); len(routed) != 0 {
		t.Fatalf("router called %v, want none on a curated borrow (instant)", routed)
	}
}

func TestPoliticalCuratedVerdictUnverifiableHasNoCitation(t *testing.T) {
	m := domain.PoliticalClaimMatch{
		ID:             "imm-x",
		LiteralVerdict: domain.LiteralUnverifiable,
		SourceURL:      "https://example.test",
		Distance:       0,
	}
	verdict := politicalCuratedVerdict(m)
	if verdict.Verdict != VerdictUnverifiable {
		t.Errorf("Verdict = %q, want unverifiable", verdict.Verdict)
	}
	if len(verdict.Citations) != 0 {
		t.Errorf("Citations = %d, want 0 (unverifiable grounds nothing)", len(verdict.Citations))
	}
}

func TestSimilarityFromDistance(t *testing.T) {
	tests := []struct {
		distance float32
		want     float64
	}{
		{0, 1},
		{2, -1},
		{0.3, 0.7},
	}
	for _, tc := range tests {
		if got := similarityFromDistance(tc.distance); math.Abs(got-tc.want) > 1e-6 {
			t.Errorf("similarityFromDistance(%v) = %v, want %v", tc.distance, got, tc.want)
		}
	}
}
