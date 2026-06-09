// Package service holds business logic. It depends only on domain interfaces.
package service

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// HealthChecker reports whether dependencies are reachable.
type HealthChecker struct {
	store domain.VectorStore
}

// NewHealthChecker builds a HealthChecker over the given vector store.
func NewHealthChecker(store domain.VectorStore) *HealthChecker {
	return &HealthChecker{store: store}
}

// Check returns nil when all dependencies are healthy.
func (h *HealthChecker) Check(ctx context.Context) error {
	if err := h.store.Ping(ctx); err != nil {
		return fmt.Errorf("vector store: %w", err)
	}
	return nil
}
