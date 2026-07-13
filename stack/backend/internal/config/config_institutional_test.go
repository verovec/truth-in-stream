package config

import (
	"testing"
	"time"
)

func TestParseLegifranceArticles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want []LegifranceArticle
	}{
		{"empty", "", nil},
		{"blank", "  ", nil},
		{"id only", "LEGIARTI1", []LegifranceArticle{{ID: "LEGIARTI1"}}},
		{"id and label", "LEGIARTI1=Code du travail", []LegifranceArticle{{ID: "LEGIARTI1", Label: "Code du travail"}}},
		{
			"list with spaces and gaps",
			" LEGIARTI1=Code du travail , LEGIARTI2 ,, LEGIARTI3=CESEDA ",
			[]LegifranceArticle{
				{ID: "LEGIARTI1", Label: "Code du travail"},
				{ID: "LEGIARTI2"},
				{ID: "LEGIARTI3", Label: "CESEDA"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseLegifranceArticles(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d articles, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("article %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoadLegifranceDefaults(t *testing.T) {
	t.Setenv("LEGIFRANCE_CLIENT_ID", "")
	t.Setenv("LEGIFRANCE_CLIENT_SECRET", "")
	cfg, err := LoadLegifrance()
	if err != nil {
		t.Fatalf("LoadLegifrance: %v", err)
	}
	if cfg.MinInterval != 500*time.Millisecond {
		t.Errorf("default MinInterval = %v, want 500ms", cfg.MinInterval)
	}
	if cfg.ManifestPath != "/state/legifrance-manifest.json" {
		t.Errorf("ManifestPath = %q", cfg.ManifestPath)
	}
	if cfg.ClientID != "" || cfg.ClientSecret != "" {
		t.Errorf("expected empty credentials by default")
	}
}

func TestLoadInstitutionalStatePaths(t *testing.T) {
	t.Parallel()
	vp, err := LoadViePublique()
	if err != nil {
		t.Fatalf("LoadViePublique: %v", err)
	}
	if vp.MarkerPath != "/state/viepublique-marker.json" || vp.ManifestPath != "/state/viepublique-manifest.json" {
		t.Errorf("vie-publique paths = %+v", vp)
	}
	h, err := LoadHATVP()
	if err != nil {
		t.Fatalf("LoadHATVP: %v", err)
	}
	if h.MarkerPath != "/state/hatvp-marker.json" || h.ManifestPath != "/state/hatvp-manifest.json" {
		t.Errorf("hatvp paths = %+v", h)
	}
}
