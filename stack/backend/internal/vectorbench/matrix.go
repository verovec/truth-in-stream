package vectorbench

import (
	"errors"
	"fmt"
	"slices"
)

// Scenario names an index strategy under measurement.
type Scenario string

const (
	// ScenarioHNSW is the production baseline: one halfvec HNSW index.
	ScenarioHNSW Scenario = "hnsw"
	// ScenarioBQ is binary quantization: a coarse bit index gathers
	// candidates by Hamming distance, then a rerank on the halfvec column
	// restores cosine ordering.
	ScenarioBQ Scenario = "bq-rerank"
	// ScenarioPartitioned is the same corpus partitioned by source with one
	// HNSW index per partition.
	ScenarioPartitioned Scenario = "partitioned"
)

// Filter names the query access pattern.
type Filter string

const (
	// FilterNone is the global search: best evidence across all sources.
	FilterNone Filter = "none"
	// FilterSource scopes the search to a single source, the pattern that
	// partition pruning favors and a global index must survive.
	FilterSource Filter = "source"
)

// Iterative scan modes accepted by hnsw.iterative_scan (pgvector >= 0.8.0).
const (
	IterativeOff     = "off"
	IterativeRelaxed = "relaxed_order"
	IterativeStrict  = "strict_order"
)

var knownScenarios = []Scenario{ScenarioHNSW, ScenarioBQ, ScenarioPartitioned}

var knownFilters = []Filter{FilterNone, FilterSource}

var knownIterative = []string{IterativeOff, IterativeRelaxed, IterativeStrict}

// Cell is one benchmark measurement: a scenario under one combination of
// query-time settings. Multiplier is the coarse candidate pool as a multiple
// of k; it is zero for every scenario except ScenarioBQ.
type Cell struct {
	Scenario   Scenario
	Filter     Filter
	EfSearch   int
	Iterative  string
	Multiplier int
}

// Label renders the cell as a stable one-line identifier for reports.
func (c Cell) Label() string {
	s := fmt.Sprintf("%s filter=%s ef=%d iter=%s", c.Scenario, c.Filter, c.EfSearch, c.Iterative)
	if c.Multiplier > 0 {
		s += fmt.Sprintf(" mult=%d", c.Multiplier)
	}
	return s
}

// MatrixConfig spans the measurement space. Multipliers applies only to
// ScenarioBQ and is required only when that scenario is present.
type MatrixConfig struct {
	Scenarios   []Scenario
	Filters     []Filter
	EfSearch    []int
	Iterative   []string
	Multipliers []int
}

func (c MatrixConfig) validate() error {
	if len(c.Scenarios) == 0 {
		return errors.New("vectorbench: no scenarios configured")
	}
	if len(c.Filters) == 0 {
		return errors.New("vectorbench: no filters configured")
	}
	if len(c.EfSearch) == 0 {
		return errors.New("vectorbench: no ef_search values configured")
	}
	if len(c.Iterative) == 0 {
		return errors.New("vectorbench: no iterative scan modes configured")
	}
	for _, s := range c.Scenarios {
		if !slices.Contains(knownScenarios, s) {
			return fmt.Errorf("vectorbench: unknown scenario %q", s)
		}
	}
	for _, f := range c.Filters {
		if !slices.Contains(knownFilters, f) {
			return fmt.Errorf("vectorbench: unknown filter %q", f)
		}
	}
	for _, mode := range c.Iterative {
		if !slices.Contains(knownIterative, mode) {
			return fmt.Errorf("vectorbench: unknown iterative scan mode %q", mode)
		}
	}
	for _, ef := range c.EfSearch {
		if ef <= 0 {
			return fmt.Errorf("vectorbench: ef_search must be positive, got %d", ef)
		}
	}
	if slices.Contains(c.Scenarios, ScenarioBQ) {
		if len(c.Multipliers) == 0 {
			return errors.New("vectorbench: bq-rerank scenario needs candidate multipliers")
		}
		for _, m := range c.Multipliers {
			if m <= 0 {
				return fmt.Errorf("vectorbench: multiplier must be positive, got %d", m)
			}
		}
	}
	return nil
}

// ExpandMatrix expands the config into the full list of measurement cells in
// a deterministic order: scenario, then filter, then ef_search, then
// iterative mode, then (for bq-rerank) multiplier.
func ExpandMatrix(cfg MatrixConfig) ([]Cell, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	var cells []Cell
	for _, scenario := range cfg.Scenarios {
		multipliers := []int{0}
		if scenario == ScenarioBQ {
			multipliers = cfg.Multipliers
		}
		for _, filter := range cfg.Filters {
			for _, ef := range cfg.EfSearch {
				for _, mode := range cfg.Iterative {
					for _, mult := range multipliers {
						cells = append(cells, Cell{
							Scenario:   scenario,
							Filter:     filter,
							EfSearch:   ef,
							Iterative:  mode,
							Multiplier: mult,
						})
					}
				}
			}
		}
	}
	return cells, nil
}
