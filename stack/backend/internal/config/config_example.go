package config

import "math"

// defaultExampleMaxItems bounds the placeholder chunks the example producer
// publishes per run; it is small because the example exists only as a copyable
// reference, never to ingest real data.
const defaultExampleMaxItems = 3

// defaultExampleQueueName is a dedicated queue no production worker drains, so the
// template producer cannot reach the live crawl.chunks queue or the production
// evidence_chunks corpus even if its binary is hand-run.
const defaultExampleQueueName = "example.evidence"

// LoadExampleQueue reads the broker configuration for the template producer's
// isolated queue. It shares the broker loader with the real sources but binds to a
// queue nothing consumes, so example jobs are inert.
func LoadExampleQueue() (Queue, error) {
	return loadQueue("RABBITMQ_EXAMPLE_QUEUE", defaultExampleQueueName)
}

// Example configures the in-tree example connector (the recipe template). Label
// is the human-readable run scope forwarded as EXAMPLE_LABEL; MaxItems bounds the
// placeholder chunks a run publishes.
type Example struct {
	Label    string
	MaxItems int
}

// LoadExample reads the example connector configuration from the environment.
// EXAMPLE_LABEL is required (it is the source's RequiredEnv, mirroring how a real
// source declares a required non-secret producer knob); EXAMPLE_MAX_ITEMS is an
// optional bound defaulting to defaultExampleMaxItems.
func LoadExample() (Example, error) {
	label, err := requireEnv("EXAMPLE_LABEL")
	if err != nil {
		return Example{}, err
	}
	maxItems, err := intEnv("EXAMPLE_MAX_ITEMS", defaultExampleMaxItems, 1, math.MaxInt32)
	if err != nil {
		return Example{}, err
	}
	return Example{Label: label, MaxItems: maxItems}, nil
}
