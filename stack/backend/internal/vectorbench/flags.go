package vectorbench

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ParseInts parses a comma-separated list of integers, as passed on the
// command line for ef_search and multiplier sweeps.
func ParseInts(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	values := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("vectorbench: parsing int list %q: %w", s, err)
		}
		values = append(values, n)
	}
	return values, nil
}

// ParseScenarios parses a comma-separated scenario list.
func ParseScenarios(s string) ([]Scenario, error) {
	parts := strings.Split(s, ",")
	values := make([]Scenario, 0, len(parts))
	for _, p := range parts {
		scenario := Scenario(strings.TrimSpace(p))
		if !slices.Contains(knownScenarios, scenario) {
			return nil, fmt.Errorf("vectorbench: unknown scenario %q", scenario)
		}
		values = append(values, scenario)
	}
	return values, nil
}

// ParseFilters parses a comma-separated filter list.
func ParseFilters(s string) ([]Filter, error) {
	parts := strings.Split(s, ",")
	values := make([]Filter, 0, len(parts))
	for _, p := range parts {
		filter := Filter(strings.TrimSpace(p))
		if !slices.Contains(knownFilters, filter) {
			return nil, fmt.Errorf("vectorbench: unknown filter %q", filter)
		}
		values = append(values, filter)
	}
	return values, nil
}

// ParseIterative parses a comma-separated list of hnsw.iterative_scan modes.
func ParseIterative(s string) ([]string, error) {
	parts := strings.Split(s, ",")
	values := make([]string, 0, len(parts))
	for _, p := range parts {
		mode := strings.TrimSpace(p)
		if !slices.Contains(knownIterative, mode) {
			return nil, fmt.Errorf("vectorbench: unknown iterative scan mode %q", mode)
		}
		values = append(values, mode)
	}
	return values, nil
}
