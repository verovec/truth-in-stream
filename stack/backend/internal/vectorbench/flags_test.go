package vectorbench

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseInts(t *testing.T) {
	t.Parallel()
	got, err := ParseInts("40, 100,200")
	if err != nil {
		t.Fatalf("ParseInts: %v", err)
	}
	if diff := cmp.Diff([]int{40, 100, 200}, got); diff != "" {
		t.Errorf("ParseInts mismatch (-want +got):\n%s", diff)
	}
	for _, bad := range []string{"", "40,,100", "40,abc"} {
		if _, err := ParseInts(bad); err == nil {
			t.Errorf("ParseInts(%q) accepted invalid input", bad)
		}
	}
}

func TestParseScenarios(t *testing.T) {
	t.Parallel()
	got, err := ParseScenarios("hnsw,bq-rerank,partitioned")
	if err != nil {
		t.Fatalf("ParseScenarios: %v", err)
	}
	want := []Scenario{ScenarioHNSW, ScenarioBQ, ScenarioPartitioned}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ParseScenarios mismatch (-want +got):\n%s", diff)
	}
	if _, err := ParseScenarios("hnsw,ivfflat"); err == nil {
		t.Error("ParseScenarios accepted an unknown scenario")
	}
}

func TestParseFilters(t *testing.T) {
	t.Parallel()
	got, err := ParseFilters("none,source")
	if err != nil {
		t.Fatalf("ParseFilters: %v", err)
	}
	if diff := cmp.Diff([]Filter{FilterNone, FilterSource}, got); diff != "" {
		t.Errorf("ParseFilters mismatch (-want +got):\n%s", diff)
	}
	if _, err := ParseFilters("none,date"); err == nil {
		t.Error("ParseFilters accepted an unknown filter")
	}
}

func TestParseIterative(t *testing.T) {
	t.Parallel()
	got, err := ParseIterative("off,relaxed_order")
	if err != nil {
		t.Fatalf("ParseIterative: %v", err)
	}
	if diff := cmp.Diff([]string{IterativeOff, IterativeRelaxed}, got); diff != "" {
		t.Errorf("ParseIterative mismatch (-want +got):\n%s", diff)
	}
	if _, err := ParseIterative("sometimes"); err == nil {
		t.Error("ParseIterative accepted an unknown mode")
	}
}
