package main

import (
	"context"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsarchive"
)

// namePub is a no-op Publisher for constructing a producer purely to read its
// Name(); no run is performed, so it never publishes.
type namePub struct{}

func (namePub) Publish(context.Context, []byte, uint8) error { return nil }

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

// TestProducerNamesMatchDescriptors guards that each schedulable source's producer
// self-identifies as its descriptor Name. The scheduler resolves enable/cron by
// descriptor Name and the run alerts key off producer.Name(); a divergence would
// alert under a name the operator never configured. The runtime guard in run()
// only fires for an enabled+built source, so this test covers the check offline.
func TestProducerNamesMatchDescriptors(t *testing.T) {
	t.Parallel()

	scrutins, err := scrutinsarchive.New(scrutinsarchive.Config{Legislature: "17", MaxPriority: 1}, namePub{}, nil)
	if err != nil {
		t.Fatalf("build scrutins producer: %v", err)
	}
	producerNames := map[string]string{
		"wikipedia": wikiProducer{}.Name(),
		"factcheck": factcheckProducer{}.Name(),
		"scrutins":  scrutins.Name(),
	}

	for _, d := range connector.All() {
		if !d.Schedulable() {
			continue
		}
		got, ok := producerNames[d.Name]
		if !ok {
			t.Errorf("no producer-name check for schedulable source %q (add one)", d.Name)
			continue
		}
		if got != d.Name {
			t.Errorf("producer for source %q names itself %q", d.Name, got)
		}
	}
}
