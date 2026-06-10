package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestSegmentMatchUnmarshalDefaultsKindToClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want domain.SegmentMatch
	}{
		{
			name: "legacy match without kind reads back as a claim",
			raw:  `{"claim":"the wall is old","verdict":"corroborates","sources":[{"title":"Src","url":"https://e.x"}],"similarity":0.9}`,
			want: domain.SegmentMatch{
				Kind:       domain.MatchKindClaim,
				Claim:      "the wall is old",
				Verdict:    domain.VerdictCorroborates,
				Sources:    []domain.Source{{Title: "Src", URL: "https://e.x"}},
				Similarity: 0.9,
			},
		},
		{
			name: "explicit claim kind is preserved",
			raw:  `{"kind":"claim","claim":"x","verdict":"unclear","sources":[],"similarity":0.5}`,
			want: domain.SegmentMatch{
				Kind:       domain.MatchKindClaim,
				Claim:      "x",
				Verdict:    domain.VerdictUnclear,
				Sources:    []domain.Source{},
				Similarity: 0.5,
			},
		},
		{
			name: "evidence kind carries article and no verdict",
			raw:  `{"kind":"evidence","claim":"lead section text","sources":[],"similarity":0.71,"article":{"title":"Great Wall","url":"https://en.wikipedia.org/wiki/Great_Wall"}}`,
			want: domain.SegmentMatch{
				Kind:       domain.MatchKindEvidence,
				Claim:      "lead section text",
				Sources:    []domain.Source{},
				Similarity: 0.71,
				Article:    &domain.Article{Title: "Great Wall", URL: "https://en.wikipedia.org/wiki/Great_Wall"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got domain.SegmentMatch
			if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("decoded match mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSegmentMatchJSONRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		match domain.SegmentMatch
		// wantKeys asserts the verdict key is omitted for evidence and present
		// for claims, the contract the frontend relies on.
		wantVerdictKey bool
		wantArticleKey bool
	}{
		{
			name: "claim serializes verdict and sources, no article",
			match: domain.SegmentMatch{
				Kind:       domain.MatchKindClaim,
				Claim:      "x",
				Verdict:    domain.VerdictContradicts,
				Sources:    []domain.Source{{Title: "t", URL: "https://u"}},
				Similarity: 0.8,
			},
			wantVerdictKey: true,
		},
		{
			name: "evidence omits verdict and includes article",
			match: domain.SegmentMatch{
				Kind:       domain.MatchKindEvidence,
				Claim:      "snippet",
				Sources:    []domain.Source{},
				Similarity: 0.6,
				Article:    &domain.Article{Title: "Title", URL: "https://w"},
			},
			wantArticleKey: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.match)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var generic map[string]json.RawMessage
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatalf("Unmarshal generic: %v", err)
			}
			if _, ok := generic["kind"]; !ok {
				t.Errorf("kind key missing in %s", raw)
			}
			if _, ok := generic["sources"]; !ok {
				t.Errorf("sources key missing in %s", raw)
			}
			if _, ok := generic["verdict"]; ok != tc.wantVerdictKey {
				t.Errorf("verdict key present=%v, want %v in %s", ok, tc.wantVerdictKey, raw)
			}
			if _, ok := generic["article"]; ok != tc.wantArticleKey {
				t.Errorf("article key present=%v, want %v in %s", ok, tc.wantArticleKey, raw)
			}

			var back domain.SegmentMatch
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatalf("round-trip Unmarshal: %v", err)
			}
			if diff := cmp.Diff(tc.match, back); diff != "" {
				t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMatchKindValid(t *testing.T) {
	t.Parallel()
	for _, k := range []domain.MatchKind{domain.MatchKindClaim, domain.MatchKindEvidence} {
		if !k.Valid() {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range []domain.MatchKind{"", "bogus"} {
		if k.Valid() {
			t.Errorf("%q should be invalid", k)
		}
	}
}
