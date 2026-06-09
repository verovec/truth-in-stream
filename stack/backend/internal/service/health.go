// Package service holds business logic. It depends only on domain interfaces.
package service

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// HealthChecker reports whether dependencies are reachable.
type HealthChecker struct {
	graph domain.GraphRepository
}

// NewHealthChecker builds a HealthChecker over the given graph repository.
func NewHealthChecker(graph domain.GraphRepository) *HealthChecker {
	return &HealthChecker{graph: graph}
}

// Check returns nil when all dependencies are healthy.
func (h *HealthChecker) Check(ctx context.Context) error {
	if err := h.graph.Ping(ctx); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	return nil
}
