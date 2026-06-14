package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/workerlifecycle"
)

// taskSetManager reads a service's task sets and deletes drained ones.
type taskSetManager interface {
	// DescribeCleanupState returns the service's desired count, its PRIMARY task
	// set (HasPrimary false when none is serving yet), and every non-PRIMARY task
	// set with its consumed queue version resolved from its task definition.
	DescribeCleanupState(ctx context.Context, service string) (cleanupState, error)
	// DeleteTaskSet force-deletes a task set the cleanup logic has cleared for
	// retirement.
	DeleteTaskSet(ctx context.Context, service, taskSetID string) error
}

// cleanupState is the live task-set state of one worker service.
type cleanupState struct {
	DesiredCount int
	HasPrimary   bool
	Primary      workerlifecycle.PrimaryTaskSet
	NonPrimary   []workerlifecycle.TaskSet
}

// runCleanup retires drained old-version task sets for each configured service.
// It never deletes a task set while the PRIMARY is still coming up (SafeToRetire),
// and never deletes a different-version set before its queues have drained
// (RetirableVersions), so a version roll never drops in-flight work. Errors are
// collected per service so one stuck service does not block cleanup of the rest.
func runCleanup(ctx context.Context, fetcher depthFetcher, mgr taskSetManager, scaling map[string]workerlifecycle.ServiceScaling, policy workerlifecycle.RetirePolicy, now time.Time, logger *slog.Logger) error {
	if len(scaling) == 0 {
		return nil
	}
	depths, err := fetchDepths(ctx, fetcher)
	if err != nil {
		return err
	}

	var errs []error
	for _, service := range sortedKeys(scaling) {
		if err := cleanupService(ctx, mgr, service, scaling[service], depths, policy, now, logger); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cleanupService(ctx context.Context, mgr taskSetManager, service string, sc workerlifecycle.ServiceScaling, depths map[string]int64, policy workerlifecycle.RetirePolicy, now time.Time, logger *slog.Logger) error {
	state, err := mgr.DescribeCleanupState(ctx, service)
	if err != nil {
		// Same skip-not-fail rule as scaling: a configured service the cluster
		// lacks is not a cleanup error.
		if errors.Is(err, errServiceMissing) {
			return nil
		}
		return err
	}
	if !state.HasPrimary || len(state.NonPrimary) == 0 {
		return nil
	}
	if !workerlifecycle.SafeToRetire(state.Primary.RunningCount, state.DesiredCount) {
		logger.InfoContext(
			ctx, "workerlifecycle: primary not healthy, preserving task sets",
			slog.String("service", service),
			slog.Int("running", state.Primary.RunningCount),
			slog.Int("desired", state.DesiredCount),
		)
		return nil
	}

	retirable := toSet(workerlifecycle.RetirableVersions(depths, sc.QueueBase))
	// Belt and suspenders: the live PRIMARY's version is never retirable, even if
	// the broker briefly exposes a newer versioned queue (e.g. the producer
	// pre-declared the next version before its task set was promoted), which would
	// otherwise make RetirableVersions treat the serving version as old.
	delete(retirable, state.Primary.Version)

	var errs []error
	for _, ts := range state.NonPrimary {
		reason, ok := workerlifecycle.RetireReason(ts, state.Primary, retirable, policy, now)
		if !ok {
			continue
		}
		if err := mgr.DeleteTaskSet(ctx, service, ts.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		logger.InfoContext(
			ctx, "workerlifecycle: retired task set",
			slog.String("service", service),
			slog.String("task_set", ts.ID),
			slog.String("reason", reason),
		)
	}
	return errors.Join(errs...)
}

func toSet(versions []string) map[string]bool {
	set := make(map[string]bool, len(versions))
	for _, v := range versions {
		set[v] = true
	}
	return set
}
