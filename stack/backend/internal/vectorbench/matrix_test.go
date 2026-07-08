package vectorbench

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func validMatrixConfig() MatrixConfig {
	return MatrixConfig{
		Scenarios:   []Scenario{ScenarioHNSW, ScenarioBQ, ScenarioPartitioned},
		Filters:     []Filter{FilterNone, FilterSource},
		EfSearch:    []int{40, 100},
		Iterative:   []string{IterativeOff, IterativeRelaxed},
		Multipliers: []int{5, 10},
	}
}

func TestExpandMatrixCellCounts(t *testing.T) {
	t.Parallel()
	cells, err := ExpandMatrix(validMatrixConfig())
	if err != nil {
		t.Fatalf("ExpandMatrix: %v", err)
	}
	counts := map[Scenario]int{}
	for _, c := range cells {
		counts[c.Scenario]++
	}
	// hnsw and partitioned: filters x ef x iterative; bq additionally x multipliers.
	if got, want := counts[ScenarioHNSW], 2*2*2; got != want {
		t.Errorf("hnsw cells = %d, want %d", got, want)
	}
	if got, want := counts[ScenarioPartitioned], 2*2*2; got != want {
		t.Errorf("partitioned cells = %d, want %d", got, want)
	}
	if got, want := counts[ScenarioBQ], 2*2*2*2; got != want {
		t.Errorf("bq cells = %d, want %d", got, want)
	}
}

func TestExpandMatrixMultiplierOnlyOnBQ(t *testing.T) {
	t.Parallel()
	cells, err := ExpandMatrix(validMatrixConfig())
	if err != nil {
		t.Fatalf("ExpandMatrix: %v", err)
	}
	for _, c := range cells {
		if c.Scenario == ScenarioBQ && c.Multiplier == 0 {
			t.Errorf("bq cell %+v has no multiplier", c)
		}
		if c.Scenario != ScenarioBQ && c.Multiplier != 0 {
			t.Errorf("non-bq cell %+v carries a multiplier", c)
		}
	}
}

func TestExpandMatrixDeterministicOrder(t *testing.T) {
	t.Parallel()
	a, err := ExpandMatrix(validMatrixConfig())
	if err != nil {
		t.Fatalf("ExpandMatrix: %v", err)
	}
	b, err := ExpandMatrix(validMatrixConfig())
	if err != nil {
		t.Fatalf("ExpandMatrix: %v", err)
	}
	if diff := cmp.Diff(a, b); diff != "" {
		t.Errorf("ExpandMatrix order not deterministic (-first +second):\n%s", diff)
	}
}

func TestExpandMatrixRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*MatrixConfig)
	}{
		{"no scenarios", func(c *MatrixConfig) { c.Scenarios = nil }},
		{"no filters", func(c *MatrixConfig) { c.Filters = nil }},
		{"no ef_search values", func(c *MatrixConfig) { c.EfSearch = nil }},
		{"no iterative modes", func(c *MatrixConfig) { c.Iterative = nil }},
		{"bq without multipliers", func(c *MatrixConfig) { c.Multipliers = nil }},
		{"unknown scenario", func(c *MatrixConfig) { c.Scenarios = []Scenario{"ivfflat"} }},
		{"unknown iterative mode", func(c *MatrixConfig) { c.Iterative = []string{"eventually"} }},
		{"non-positive ef_search", func(c *MatrixConfig) { c.EfSearch = []int{0} }},
		{"non-positive multiplier", func(c *MatrixConfig) { c.Multipliers = []int{-1} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validMatrixConfig()
			tc.mutate(&cfg)
			if _, err := ExpandMatrix(cfg); err == nil {
				t.Error("ExpandMatrix accepted an invalid config")
			}
		})
	}
}

func TestExpandMatrixWithoutBQNeedsNoMultipliers(t *testing.T) {
	t.Parallel()
	cfg := validMatrixConfig()
	cfg.Scenarios = []Scenario{ScenarioHNSW}
	cfg.Multipliers = nil
	cells, err := ExpandMatrix(cfg)
	if err != nil {
		t.Fatalf("ExpandMatrix: %v", err)
	}
	if len(cells) != 2*2*2 {
		t.Errorf("got %d cells, want %d", len(cells), 2*2*2)
	}
}

func TestCellLabel(t *testing.T) {
	t.Parallel()
	c := Cell{Scenario: ScenarioBQ, Filter: FilterSource, EfSearch: 100, Iterative: IterativeRelaxed, Multiplier: 5}
	want := "bq-rerank filter=source ef=100 iter=relaxed_order mult=5"
	if got := c.Label(); got != want {
		t.Errorf("Label() = %q, want %q", got, want)
	}
	plain := Cell{Scenario: ScenarioHNSW, Filter: FilterNone, EfSearch: 40, Iterative: IterativeOff}
	wantPlain := "hnsw filter=none ef=40 iter=off"
	if got := plain.Label(); got != wantPlain {
		t.Errorf("Label() = %q, want %q", got, wantPlain)
	}
}
