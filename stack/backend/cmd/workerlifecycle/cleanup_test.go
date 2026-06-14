package main

import (
	"context"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/mqmetrics"
	"github.com/verovec/truth-in-stream/backend/internal/workerlifecycle"
)

type fakeTaskSetManager struct {
	state    cleanupState
	stateErr error
	deleted  []string
}

func (f *fakeTaskSetManager) DescribeCleanupState(context.Context, string) (cleanupState, error) {
	return f.state, f.stateErr
}

func (f *fakeTaskSetManager) DeleteTaskSet(_ context.Context, _, taskSetID string) error {
	f.deleted = append(f.deleted, taskSetID)
	return nil
}

func TestRunCleanup(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	scaling := map[string]workerlifecycle.ServiceScaling{
		"embedworker": {QueueBase: "embedding.jobs", Ratio: 100, Min: 0, Max: 10},
	}
	policy := workerlifecycle.RetirePolicy{
		MaxAge:            30 * time.Minute,
		SameVersionMinAge: 2 * time.Minute,
		ZombieMinAge:      15 * time.Minute,
	}
	// v1 drained, v2 is current with backlog.
	depths := []mqmetrics.APIQueue{
		{Name: "embedding.jobs.v1", Messages: 0},
		{Name: "embedding.jobs.v2", Messages: 50},
	}

	t.Run("retires drained old-version task set once primary aged", func(t *testing.T) {
		t.Parallel()
		mgr := &fakeTaskSetManager{state: cleanupState{
			DesiredCount: 2,
			HasPrimary:   true,
			Primary:      workerlifecycle.PrimaryTaskSet{Version: "2", CreatedAt: now.Add(-1 * time.Hour), RunningCount: 2},
			NonPrimary: []workerlifecycle.TaskSet{
				{ID: "old", Version: "1", CreatedAt: now.Add(-50 * time.Minute), RunningCount: 1},
			},
		}}
		if err := runCleanup(context.Background(), fakeFetcher{queues: depths}, mgr, scaling, policy, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mgr.deleted) != 1 || mgr.deleted[0] != "old" {
			t.Fatalf("expected to retire 'old', got %v", mgr.deleted)
		}
	})

	t.Run("preserves task sets while primary is unhealthy", func(t *testing.T) {
		t.Parallel()
		mgr := &fakeTaskSetManager{state: cleanupState{
			DesiredCount: 3,
			HasPrimary:   true,
			Primary:      workerlifecycle.PrimaryTaskSet{Version: "2", CreatedAt: now.Add(-1 * time.Hour), RunningCount: 1},
			NonPrimary: []workerlifecycle.TaskSet{
				{ID: "old", Version: "1", CreatedAt: now.Add(-50 * time.Minute), RunningCount: 1},
			},
		}}
		if err := runCleanup(context.Background(), fakeFetcher{queues: depths}, mgr, scaling, policy, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mgr.deleted) != 0 {
			t.Fatalf("expected no retirement while primary unhealthy, got %v", mgr.deleted)
		}
	})

	t.Run("keeps undrained old-version task set", func(t *testing.T) {
		t.Parallel()
		undrained := []mqmetrics.APIQueue{
			{Name: "embedding.jobs.v1", Messages: 7}, // still has work
			{Name: "embedding.jobs.v2", Messages: 50},
		}
		mgr := &fakeTaskSetManager{state: cleanupState{
			DesiredCount: 2,
			HasPrimary:   true,
			Primary:      workerlifecycle.PrimaryTaskSet{Version: "2", CreatedAt: now.Add(-1 * time.Hour), RunningCount: 2},
			NonPrimary: []workerlifecycle.TaskSet{
				{ID: "old", Version: "1", CreatedAt: now.Add(-50 * time.Minute), RunningCount: 1},
			},
		}}
		if err := runCleanup(context.Background(), fakeFetcher{queues: undrained}, mgr, scaling, policy, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mgr.deleted) != 0 {
			t.Fatalf("expected to keep undrained task set, got %v", mgr.deleted)
		}
	})

	t.Run("missing service is skipped, not an error", func(t *testing.T) {
		t.Parallel()
		mgr := &fakeTaskSetManager{stateErr: errServiceMissing}
		if err := runCleanup(context.Background(), fakeFetcher{queues: depths}, mgr, scaling, policy, now, discardLogger()); err != nil {
			t.Fatalf("expected missing service to be skipped, got %v", err)
		}
		if len(mgr.deleted) != 0 {
			t.Fatalf("expected no deletions for a missing service, got %v", mgr.deleted)
		}
	})

	t.Run("no primary is a no-op", func(t *testing.T) {
		t.Parallel()
		mgr := &fakeTaskSetManager{state: cleanupState{HasPrimary: false}}
		if err := runCleanup(context.Background(), fakeFetcher{queues: depths}, mgr, scaling, policy, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mgr.deleted) != 0 {
			t.Fatalf("expected no deletions without a primary, got %v", mgr.deleted)
		}
	})
}
