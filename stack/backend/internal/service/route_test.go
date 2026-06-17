package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/claimtype"
	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// fakeRetriever is a programmable source.Retriever for routing tests: it records
// the queries it was asked and returns a scripted result. It never makes a real
// outbound call.
type fakeRetriever struct {
	kind     source.Kind
	evidence []source.Evidence
	err      error
	calls    []source.Query
}

func (f *fakeRetriever) Kind() source.Kind { return f.kind }

func (f *fakeRetriever) Retrieve(_ context.Context, q source.Query) ([]source.Evidence, error) {
	f.calls = append(f.calls, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.evidence, nil
}

func ev(kind source.Kind, id, passage string) source.Evidence {
	return source.Evidence{
		ID:      source.NewEvidenceID(kind, id, 0),
		Passage: passage,
		Source:  source.Source{Name: string(kind), URL: "https://example.test/" + id},
	}
}

func newRouterT(t *testing.T, retrievers []source.Retriever, cfg RouterConfig) *Router {
	t.Helper()
	r, err := NewRouter(retrievers, cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

func TestRouterRoutesByClaimType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claimType claimtype.Type
		// wantKind is the adapter that must be hit; "" means none.
		wantKind source.Kind
	}{
		{name: "statistic routes to stats", claimType: claimtype.Statistic, wantKind: source.KindStats},
		{name: "voting routes to voting", claimType: claimtype.VotingRecord, wantKind: source.KindVotingRecord},
		{name: "attribution routes to press", claimType: claimtype.Attribution, wantKind: source.KindAttribution},
		{name: "causal routes to web", claimType: claimtype.Causal, wantKind: source.KindWebSearch},
		{name: "comparative routes to web", claimType: claimtype.Comparative, wantKind: source.KindWebSearch},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stats := &fakeRetriever{kind: source.KindStats, evidence: []source.Evidence{ev(source.KindStatsINSEE, "S1", "serie")}}
			voting := &fakeRetriever{kind: source.KindVotingRecord, evidence: []source.Evidence{ev(source.KindVotingRecord, "V1", "a vote pour")}}
			press := &fakeRetriever{kind: source.KindAttribution, evidence: []source.Evidence{ev(source.KindAttribution, "lemonde.fr", "a declare")}}
			web := &fakeRetriever{kind: source.KindWebSearch, evidence: []source.Evidence{ev(source.KindWebSearch, "wikipedia.fr", "contexte")}}

			r := newRouterT(t, []source.Retriever{stats, voting, press, web}, RouterConfig{MinResults: 1})

			out, err := r.Retrieve(t.Context(), "une affirmation", tc.claimType, nil)
			if err != nil {
				t.Fatalf("Retrieve: %v", err)
			}
			if len(out) == 0 {
				t.Fatalf("Retrieve returned no evidence")
			}

			byKind := map[source.Kind]int{
				source.KindStats:        len(stats.calls),
				source.KindVotingRecord: len(voting.calls),
				source.KindAttribution:  len(press.calls),
				source.KindWebSearch:    len(web.calls),
			}
			if byKind[tc.wantKind] == 0 {
				t.Fatalf("claim type %q did not hit adapter %q (calls: %v)", tc.claimType, tc.wantKind, byKind)
			}
			// The primary adapter answered, so the web fallback must not have fired
			// unless web is itself the primary adapter.
			if tc.wantKind != source.KindWebSearch && len(web.calls) != 0 {
				t.Fatalf("web fallback fired despite a non-thin primary result (web calls: %d)", len(web.calls))
			}
		})
	}
}

func TestRouterStatisticReturnsSeriesNotPoint(t *testing.T) {
	t.Parallel()

	// The stats adapter renders a whole series into one passage; the router must
	// surface it verbatim so the verifier can see adjacent periods and flag a
	// cherry-pick. A single point would defeat that.
	series := "2021: 7.4%\n2022: 7.3%\n2023: 7.1%\n2024: 7.5%"
	stats := &fakeRetriever{kind: source.KindStats, evidence: []source.Evidence{ev(source.KindStatsINSEE, "CHOMAGE-T", series)}}
	web := &fakeRetriever{kind: source.KindWebSearch}

	r := newRouterT(t, []source.Retriever{stats, web}, RouterConfig{MinResults: 1})

	out, err := r.Retrieve(t.Context(), "le chomage est a 7,5%", claimtype.Statistic, map[string]string{"insee_idbank": "CHOMAGE-T"})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d evidence, want 1", len(out))
	}
	if !strings.Contains(out[0].Passage, "2021") || !strings.Contains(out[0].Passage, "2024") {
		t.Fatalf("statistic passage is not a full series: %q", out[0].Passage)
	}
	// Hints must reach the stats adapter so it can select the series.
	if len(stats.calls) != 1 {
		t.Fatalf("stats adapter calls = %d, want 1", len(stats.calls))
	}
	if got := stats.calls[0].Hints["insee_idbank"]; got != "CHOMAGE-T" {
		t.Fatalf("stats query missing idbank hint, got %q", got)
	}
	if len(web.calls) != 0 {
		t.Fatalf("web fallback fired on a non-thin statistic result")
	}
}

func TestRouterFallsBackToWebOnThinResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		primary *fakeRetriever
	}{
		{
			name:    "empty primary broadens to web",
			primary: &fakeRetriever{kind: source.KindStats},
		},
		{
			name:    "errored primary broadens to web",
			primary: &fakeRetriever{kind: source.KindStats, err: errors.New("insee down")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			web := &fakeRetriever{kind: source.KindWebSearch, evidence: []source.Evidence{ev(source.KindWebSearch, "insee.fr", "contexte web")}}
			r := newRouterT(t, []source.Retriever{tc.primary, web}, RouterConfig{MinResults: 1})

			out, err := r.Retrieve(t.Context(), "le chomage baisse", claimtype.Statistic, nil)
			if err != nil {
				t.Fatalf("Retrieve: %v", err)
			}
			if len(web.calls) != 1 {
				t.Fatalf("web fallback did not fire on thin primary (web calls: %d)", len(web.calls))
			}
			if len(out) != 1 || out[0].ID.Kind != source.KindWebSearch {
				t.Fatalf("thin primary did not broaden to a web result: %+v", out)
			}
			// The atomic claim text is the web query.
			if web.calls[0].Text != "le chomage baisse" {
				t.Fatalf("web query = %q, want the claim text", web.calls[0].Text)
			}
		})
	}
}

func TestRouterTotalFailureSurfacesError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claimType claimtype.Type
		// retrievers wired into the router. Each errs, so nothing is retrieved.
		retrievers func() []source.Retriever
	}{
		{
			// A statistic whose stats adapter AND the web fallback both err: the
			// claim retrieves nothing and the failure must surface, not vanish.
			name:      "stats and web both fail",
			claimType: claimtype.Statistic,
			retrievers: func() []source.Retriever {
				return []source.Retriever{
					&fakeRetriever{kind: source.KindStats, err: errors.New("insee down")},
					&fakeRetriever{kind: source.KindWebSearch, err: errors.New("brave down")},
				}
			},
		},
		{
			// A causal claim routes to web as its primary adapter. When web errors,
			// there is no second source to broaden to; the error must still surface
			// rather than collapse to a silent empty result.
			name:      "web-primary claim errors",
			claimType: claimtype.Causal,
			retrievers: func() []source.Retriever {
				return []source.Retriever{
					&fakeRetriever{kind: source.KindWebSearch, err: errors.New("brave down")},
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newRouterT(t, tc.retrievers(), RouterConfig{MinResults: 1})
			out, err := r.Retrieve(t.Context(), "le chomage baisse", tc.claimType, nil)
			if err == nil {
				t.Fatalf("total retrieval failure returned no error (out: %+v)", out)
			}
			if len(out) != 0 {
				t.Fatalf("total retrieval failure returned %d evidence, want 0", len(out))
			}
		})
	}
}

func TestRouterEmptyResultIsNotAnError(t *testing.T) {
	t.Parallel()

	// Every source answers cleanly with nothing: a genuine "no evidence found" is
	// an empty result, not an error - distinct from a retrieval failure.
	stats := &fakeRetriever{kind: source.KindStats}
	web := &fakeRetriever{kind: source.KindWebSearch}
	r := newRouterT(t, []source.Retriever{stats, web}, RouterConfig{MinResults: 1})

	out, err := r.Retrieve(t.Context(), "le chomage baisse", claimtype.Statistic, nil)
	if err != nil {
		t.Fatalf("clean empty result returned an error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d evidence, want 0", len(out))
	}
	if len(web.calls) != 1 {
		t.Fatalf("web fallback should still run on a clean-but-empty primary (calls: %d)", len(web.calls))
	}
}

func TestRouterNonVerifiableTypeRetrievesNothing(t *testing.T) {
	t.Parallel()

	for _, ct := range []claimtype.Type{claimtype.Promise, claimtype.Opinion} {
		t.Run(string(ct), func(t *testing.T) {
			t.Parallel()

			stats := &fakeRetriever{kind: source.KindStats, evidence: []source.Evidence{ev(source.KindStatsINSEE, "S", "x")}}
			web := &fakeRetriever{kind: source.KindWebSearch, evidence: []source.Evidence{ev(source.KindWebSearch, "h", "y")}}
			r := newRouterT(t, []source.Retriever{stats, web}, RouterConfig{MinResults: 1})

			out, err := r.Retrieve(t.Context(), "ce sera mieux demain", ct, nil)
			if err != nil {
				t.Fatalf("Retrieve: %v", err)
			}
			if len(out) != 0 {
				t.Fatalf("non-verifiable type returned %d evidence, want 0", len(out))
			}
			if len(stats.calls)+len(web.calls) != 0 {
				t.Fatalf("non-verifiable type hit an adapter (stats=%d web=%d)", len(stats.calls), len(web.calls))
			}
		})
	}
}

func TestRouterEvidenceIDsRoundTripIntoVerifier(t *testing.T) {
	t.Parallel()

	statID := source.NewEvidenceID(source.KindStatsINSEE, "CHOMAGE-T", 0)
	stats := &fakeRetriever{kind: source.KindStats, evidence: []source.Evidence{{ID: statID, Passage: "serie", Source: source.Source{Name: "INSEE"}}}}
	web := &fakeRetriever{kind: source.KindWebSearch}
	r := newRouterT(t, []source.Retriever{stats, web}, RouterConfig{MinResults: 1})

	out, err := r.Retrieve(t.Context(), "le chomage est a 7,5%", claimtype.Statistic, map[string]string{"insee_idbank": "CHOMAGE-T"})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	// Project to verifier passages the way the capstone (card L) will, then parse
	// each id back: it must equal the id retrieval minted, unchanged.
	passages := EvidencePassagesFrom(out)
	if len(passages) != 1 {
		t.Fatalf("got %d passages, want 1", len(passages))
	}
	if passages[0].ID != statID.String() {
		t.Fatalf("passage id = %q, want %q", passages[0].ID, statID.String())
	}
	parsed, err := source.ParseEvidenceID(passages[0].ID)
	if err != nil {
		t.Fatalf("ParseEvidenceID(%q): %v", passages[0].ID, err)
	}
	if diff := cmp.Diff(statID, parsed); diff != "" {
		t.Fatalf("evidence id did not round-trip (-want +got):\n%s", diff)
	}
}

func TestRouterDeduplicatesByEvidenceID(t *testing.T) {
	t.Parallel()

	// A primary that returns just under the floor still contributes its evidence;
	// the web fallback may re-surface the same id, which must not appear twice.
	dupID := source.NewEvidenceID(source.KindWebSearch, "insee.fr", 0)
	press := &fakeRetriever{kind: source.KindAttribution, evidence: []source.Evidence{{ID: dupID, Passage: "p"}}}
	web := &fakeRetriever{kind: source.KindWebSearch, evidence: []source.Evidence{{ID: dupID, Passage: "p"}, ev(source.KindWebSearch, "lefigaro.fr", "q")}}
	r := newRouterT(t, []source.Retriever{press, web}, RouterConfig{MinResults: 2})

	out, err := r.Retrieve(t.Context(), "Z a dit", claimtype.Attribution, nil)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	seen := map[string]int{}
	for _, e := range out {
		seen[e.ID.String()]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("evidence id %q appeared %d times after dedup", id, n)
		}
	}
}

func TestRouterDropsUncitableEvidence(t *testing.T) {
	t.Parallel()

	// An adapter that returns evidence with a zero-value (unset) EvidenceID: such a
	// passage cannot be cited, so the router must drop it rather than let several
	// uncited passages collapse onto one dedup key. Two uncited passages plus one
	// proper web result must yield exactly the one citable result.
	press := &fakeRetriever{kind: source.KindAttribution, evidence: []source.Evidence{
		{Passage: "uncitable one"},
		{Passage: "uncitable two"},
	}}
	web := &fakeRetriever{kind: source.KindWebSearch, evidence: []source.Evidence{ev(source.KindWebSearch, "lemonde.fr", "citable")}}
	r := newRouterT(t, []source.Retriever{press, web}, RouterConfig{MinResults: 1})

	out, err := r.Retrieve(t.Context(), "Z a dit", claimtype.Attribution, nil)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) != 1 || out[0].ID.Kind != source.KindWebSearch {
		t.Fatalf("uncitable evidence was not dropped: %+v", out)
	}
}

func TestRouterUnknownTypeFallsBackToWeb(t *testing.T) {
	t.Parallel()

	// claimtype.DefaultType (causal) and any value with no dedicated adapter route
	// to web, the catch-all for open-ended claims.
	web := &fakeRetriever{kind: source.KindWebSearch, evidence: []source.Evidence{ev(source.KindWebSearch, "h", "ctx")}}
	r := newRouterT(t, []source.Retriever{web}, RouterConfig{MinResults: 1})

	out, err := r.Retrieve(t.Context(), "claim", claimtype.DefaultType, nil)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) != 1 || len(web.calls) != 1 {
		t.Fatalf("default type did not route to web: out=%d webcalls=%d", len(out), len(web.calls))
	}
}

func TestNewRouterValidation(t *testing.T) {
	t.Parallel()

	web := &fakeRetriever{kind: source.KindWebSearch}

	t.Run("requires a web retriever for the fallback", func(t *testing.T) {
		t.Parallel()
		stats := &fakeRetriever{kind: source.KindStats}
		if _, err := NewRouter([]source.Retriever{stats}, RouterConfig{MinResults: 1}); err == nil {
			t.Fatal("NewRouter without a web retriever should fail")
		}
	})

	t.Run("rejects a non-positive MinResults", func(t *testing.T) {
		t.Parallel()
		if _, err := NewRouter([]source.Retriever{web}, RouterConfig{MinResults: 0}); err == nil {
			t.Fatal("NewRouter with MinResults 0 should fail")
		}
	})

	t.Run("rejects duplicate retriever kinds", func(t *testing.T) {
		t.Parallel()
		web2 := &fakeRetriever{kind: source.KindWebSearch}
		if _, err := NewRouter([]source.Retriever{web, web2}, RouterConfig{MinResults: 1}); err == nil {
			t.Fatal("NewRouter with two retrievers of the same kind should fail")
		}
	})
}

func TestRouterEmptyClaimRetrievesNothing(t *testing.T) {
	t.Parallel()

	web := &fakeRetriever{kind: source.KindWebSearch, evidence: []source.Evidence{ev(source.KindWebSearch, "h", "y")}}
	r := newRouterT(t, []source.Retriever{web}, RouterConfig{MinResults: 1})

	out, err := r.Retrieve(t.Context(), "   ", claimtype.Causal, nil)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(out) != 0 || len(web.calls) != 0 {
		t.Fatalf("blank claim hit an adapter: out=%d webcalls=%d", len(out), len(web.calls))
	}
}
