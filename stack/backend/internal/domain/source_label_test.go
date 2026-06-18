package domain

import "testing"

func TestWinningSourceLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		matches   []SegmentMatch
		wantLabel string
		wantURL   string
	}{
		{
			name:      "no matches is no label",
			matches:   nil,
			wantLabel: "",
			wantURL:   "",
		},
		{
			name: "voting record names the Assemblee from its provenance",
			matches: []SegmentMatch{{
				Kind:       MatchKindEvidence,
				EvidenceID: "voting:scrutin-42:0",
				Sources:    []Source{{Title: "Assemblee nationale (scrutin)", URL: "https://an.fr/s/42"}},
			}},
			wantLabel: SourceLabelAssemblee,
			wantURL:   "https://an.fr/s/42",
		},
		{
			name: "voting record names the Senat from its provenance",
			matches: []SegmentMatch{{
				Kind:       MatchKindEvidence,
				EvidenceID: "voting:scrutin-7:0",
				Sources:    []Source{{Title: "Sénat (scrutin)", URL: "https://senat.fr/s/7"}},
			}},
			wantLabel: SourceLabelSenat,
			wantURL:   "https://senat.fr/s/7",
		},
		{
			name: "voting record with an unknown chamber falls back to a generic label",
			matches: []SegmentMatch{{
				Kind:       MatchKindEvidence,
				EvidenceID: "voting:scrutin-9:0",
				Sources:    []Source{{Title: "Scrutin parlementaire", URL: "https://parl.fr/9"}},
			}},
			wantLabel: SourceLabelParlement,
			wantURL:   "https://parl.fr/9",
		},
		{
			name: "INSEE statistic",
			matches: []SegmentMatch{{
				EvidenceID: "insee:CHOM:0",
				Sources:    []Source{{Title: "INSEE", URL: "https://insee.fr/x"}},
			}},
			wantLabel: SourceLabelINSEE,
			wantURL:   "https://insee.fr/x",
		},
		{
			name: "Eurostat statistic",
			matches: []SegmentMatch{{
				EvidenceID: "eurostat:nama:0",
				Sources:    []Source{{Title: "Eurostat", URL: "https://ec.europa.eu/x"}},
			}},
			wantLabel: SourceLabelEurostat,
			wantURL:   "https://ec.europa.eu/x",
		},
		{
			name: "web search fallback",
			matches: []SegmentMatch{{
				EvidenceID: "websearch:lemonde.fr:0",
				Sources:    []Source{{Title: "lemonde.fr", URL: "https://lemonde.fr/a"}},
			}},
			wantLabel: SourceLabelWeb,
			wantURL:   "https://lemonde.fr/a",
		},
		{
			name: "press attribution shows the outlet when named",
			matches: []SegmentMatch{{
				EvidenceID: "attribution:42:0",
				Sources:    []Source{{Title: "Le Monde", URL: "https://lemonde.fr/b"}},
			}},
			wantLabel: "Le Monde",
			wantURL:   "https://lemonde.fr/b",
		},
		{
			name: "press attribution without an outlet falls back to Presse",
			matches: []SegmentMatch{{
				EvidenceID: "attribution:42:0",
			}},
			wantLabel: SourceLabelPresse,
			wantURL:   "",
		},
		{
			name: "wikipedia evidence links the article",
			matches: []SegmentMatch{{
				Kind:       MatchKindEvidence,
				EvidenceID: "evidence:1789:0",
				Article:    &Article{Title: "Révolution", URL: "https://fr.wikipedia.org/wiki/Revolution"},
			}},
			wantLabel: SourceLabelWikipedia,
			wantURL:   "https://fr.wikipedia.org/wiki/Revolution",
		},
		{
			name: "curated borrow carries no provider label",
			matches: []SegmentMatch{{
				Kind:       MatchKindClaim,
				EvidenceID: "claim:abc:0",
				Verdict:    VerdictCorroborates,
				Sources:    []Source{{Title: "Décret", URL: "https://legifrance.fr/x"}},
			}},
			wantLabel: "",
			wantURL:   "",
		},
		{
			name: "a match with no evidence id carries no label",
			matches: []SegmentMatch{{
				Kind:    MatchKindEvidence,
				Sources: []Source{{Title: "INSEE", URL: "https://insee.fr/x"}},
			}},
			wantLabel: "",
			wantURL:   "",
		},
		{
			name: "the first labeled citation wins over a later one",
			matches: []SegmentMatch{
				{Kind: MatchKindClaim, EvidenceID: "claim:abc:0"},
				{EvidenceID: "insee:CHOM:0", Sources: []Source{{Title: "INSEE", URL: "https://insee.fr/x"}}},
				{EvidenceID: "eurostat:nama:0", Sources: []Source{{Title: "Eurostat", URL: "https://ec.europa.eu/x"}}},
			},
			wantLabel: SourceLabelINSEE,
			wantURL:   "https://insee.fr/x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := WinningSource(tt.matches)
			if got.Label != tt.wantLabel {
				t.Errorf("label = %q, want %q", got.Label, tt.wantLabel)
			}
			if got.URL != tt.wantURL {
				t.Errorf("url = %q, want %q", got.URL, tt.wantURL)
			}
		})
	}
}
