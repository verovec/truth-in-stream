package config

import (
	"slices"
	"testing"
)

func TestLoadSDMXDefaults(t *testing.T) {
	t.Setenv("SDMX_SOURCES", "")
	t.Setenv("SDMX_START_PERIOD", "")
	t.Setenv("SDMX_END_PERIOD", "")
	cfg, err := LoadSDMX()
	if err != nil {
		t.Fatalf("LoadSDMX: %v", err)
	}
	if !slices.Equal(cfg.Sources, []string{"eurostat", "ecb", "oecd"}) {
		t.Errorf("default sources = %v, want [eurostat ecb oecd]", cfg.Sources)
	}
	if cfg.Start != "" || cfg.End != "" {
		t.Errorf("default window = %q..%q, want empty", cfg.Start, cfg.End)
	}
}

func TestLoadSDMXSelectsAndDeduplicates(t *testing.T) {
	t.Setenv("SDMX_SOURCES", "ecb, ECB ,oecd")
	t.Setenv("SDMX_START_PERIOD", "2010-01")
	t.Setenv("SDMX_END_PERIOD", "2020-12")
	cfg, err := LoadSDMX()
	if err != nil {
		t.Fatalf("LoadSDMX: %v", err)
	}
	if !slices.Equal(cfg.Sources, []string{"ecb", "oecd"}) {
		t.Errorf("sources = %v, want [ecb oecd] (deduplicated, case-folded)", cfg.Sources)
	}
	if cfg.Start != "2010-01" || cfg.End != "2020-12" {
		t.Errorf("window = %q..%q", cfg.Start, cfg.End)
	}
}

func TestLoadSDMXRejectsUnknownSource(t *testing.T) {
	t.Setenv("SDMX_SOURCES", "ecb,imf")
	if _, err := LoadSDMX(); err == nil {
		t.Fatal("LoadSDMX accepted an unknown source, want error")
	}
}
