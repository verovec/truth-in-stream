package stats

import (
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestRenderFrenchDeterministic(t *testing.T) {
	tests := []struct {
		name string
		dp   domain.Datapoint
		want string
	}{
		{
			name: "residence permits integer figure",
			dp: domain.Datapoint{
				SourceName: "Eurostat",
				SourceURL:  "https://ec.europa.eu/eurostat/x",
				Dataset:    "MIGR_RESFIRST",
				SeriesKey:  "A.TOTAL.TOTAL.TOTAL.PER.FR",
				Title:      "Premiers titres de séjour délivrés",
				Geography:  "France",
				Dimensions: []string{"toutes nationalités", "tous motifs"},
				Period:     "2022",
				Figure:     326948,
				Unit:       "personnes",
			},
			want: "Premiers titres de séjour délivrés (toutes nationalités, tous motifs) en France en 2022 : 326 948 personnes. Source : Eurostat (jeu de données MIGR_RESFIRST), https://ec.europa.eu/eurostat/x",
		},
		{
			name: "employment rate percent decimal",
			dp: domain.Datapoint{
				SourceName: "Eurostat",
				SourceURL:  "https://ec.europa.eu/eurostat/y",
				Dataset:    "LFSA_ARGAN",
				SeriesKey:  "A.PC.T.Y15-64.FOR.FR",
				Title:      "Taux d'activité",
				Geography:  "France",
				Dimensions: []string{"ressortissants étrangers", "15 à 64 ans"},
				Period:     "2021",
				Figure:     66.5,
				Unit:       "%",
			},
			want: "Taux d'activité (ressortissants étrangers, 15 à 64 ans) en France en 2021 : 66,5 %. Source : Eurostat (jeu de données LFSA_ARGAN), https://ec.europa.eu/eurostat/y",
		},
		{
			name: "monthly period and no dimensions",
			dp: domain.Datapoint{
				SourceName: "Eurostat",
				SourceURL:  "https://ec.europa.eu/eurostat/z",
				Dataset:    "DS",
				SeriesKey:  "K",
				Title:      "Indicateur",
				Geography:  "Allemagne",
				Dimensions: nil,
				Period:     "2022-03",
				Figure:     1000,
				Unit:       "personnes",
			},
			want: "Indicateur en Allemagne en mars 2022 : 1 000 personnes. Source : Eurostat (jeu de données DS), https://ec.europa.eu/eurostat/z",
		},
		{
			name: "quarterly INSEE period rendered in French prose",
			dp: domain.Datapoint{
				SourceName: "Insee",
				SourceURL:  "https://bdm.insee.fr/series/sdmx/data/SERIES_BDM/001688526",
				Dataset:    "CHOMAGE-TRIM-NATIONAL",
				SeriesKey:  "001688526",
				Title:      "Taux de chômage au sens du BIT",
				Geography:  "France métropolitaine",
				Dimensions: []string{"ensemble", "15 ans ou plus"},
				Period:     "2024-Q1",
				Figure:     7.5,
				Unit:       "%",
			},
			want: "Taux de chômage au sens du BIT (ensemble, 15 ans ou plus) en France métropolitaine au 1er trimestre 2024 : 7,5 %. Source : Insee (jeu de données CHOMAGE-TRIM-NATIONAL), https://bdm.insee.fr/series/sdmx/data/SERIES_BDM/001688526",
		},
		{
			name: "empty dimensions dropped",
			dp: domain.Datapoint{
				SourceName: "Eurostat",
				SourceURL:  "https://e/x",
				Dataset:    "DS",
				SeriesKey:  "K",
				Title:      "Indicateur",
				Geography:  "France",
				Dimensions: []string{"", "valide", ""},
				Period:     "2020",
				Figure:     -5.25,
				Unit:       "%",
			},
			want: "Indicateur (valide) en France en 2020 : -5,25 %. Source : Eurostat (jeu de données DS), https://e/x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderFrench(tt.dp)
			if got != tt.want {
				t.Errorf("RenderFrench()\n got = %q\nwant = %q", got, tt.want)
			}
			// Determinism: identical input renders identically.
			if again := RenderFrench(tt.dp); again != got {
				t.Errorf("RenderFrench not deterministic: %q != %q", again, got)
			}
		})
	}
}

func TestRenderFrenchContainsProvenance(t *testing.T) {
	dp := domain.Datapoint{
		SourceName: "Eurostat", SourceURL: "https://src/url", Dataset: "DS",
		SeriesKey: "K", Title: "T", Geography: "France", Period: "2022",
		Figure: 42, Unit: "personnes",
	}
	got := RenderFrench(dp)
	for _, want := range []string{"France", "2022", "42", "https://src/url", "Eurostat"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered passage %q missing %q", got, want)
		}
	}
}
