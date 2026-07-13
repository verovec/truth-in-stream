package main

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
)

// TestBuildersCoverSchedulableSources guards the lockstep between the connector
// registry's schedulable sources and this command's builders table: every
// schedulable descriptor must have a builder (else the scheduler fails at startup
// when the source is enabled), and every builder must correspond to a schedulable
// descriptor (else it is dead wiring).
func TestBuildersCoverSchedulableSources(t *testing.T) {
	t.Parallel()

	schedulable := make(map[string]struct{})
	for _, d := range connector.All() {
		if d.Schedulable() {
			schedulable[d.Name] = struct{}{}
			if _, ok := builders[d.Name]; !ok {
				t.Errorf("schedulable source %q has no producer builder", d.Name)
			}
		}
	}
	for name := range builders {
		if _, ok := schedulable[name]; !ok {
			t.Errorf("builder %q has no schedulable descriptor in the registry", name)
		}
	}
}
