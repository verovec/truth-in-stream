// Package service holds business logic. It depends only on domain interfaces.
package service

import (
	"context"
	"fmt"
)

// Pinger is the slice of the claim store the health check needs: a single
// reachability probe. Defined here, on the consumer side, so the health check
// does not depend on the full store surface.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthChecker reports whether dependencies are reachable.
type HealthChecker struct {
	store Pinger
}

// NewHealthChecker builds a HealthChecker over the given store.
func NewHealthChecker(store Pinger) *HealthChecker {
	return &HealthChecker{store: store}
}

// Check returns nil when all dependencies are healthy.
func (h *HealthChecker) Check(ctx context.Context) error {
	if err := h.store.Ping(ctx); err != nil {
		return fmt.Errorf("claim store: %w", err)
	}
	return nil
}
