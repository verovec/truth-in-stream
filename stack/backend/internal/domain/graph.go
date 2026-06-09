// Package domain holds core models and repository interfaces (ports).
// It imports nothing internal; all layers depend inward on this package.
package domain

import "context"

// GraphRepository is the port for the graph datastore. The concrete
// implementation (store/graph) is the only place that knows about the
// embedded engine, so the engine can be swapped or extracted behind this
// interface without changing service or handler code.
type GraphRepository interface {
	// Ping verifies the graph datastore is reachable.
	Ping(ctx context.Context) error
}
