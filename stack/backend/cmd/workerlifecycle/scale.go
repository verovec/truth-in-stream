package main

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/mqmetrics"
	"github.com/verovec/truth-in-stream/backend/internal/workerlifecycle"
)

// depthFetcher reads current per-queue backlog from the broker. The mqmetrics
// management-API client satisfies it.
type depthFetcher interface {
	FetchQueues(ctx context.Context) ([]mqmetrics.APIQueue, error)
}

// serviceScaler reads and writes a worker service's desired replica count.
type serviceScaler interface {
	// DescribeServiceState returns the service's desired count, the running count
	// of its PRIMARY task set, and the time it was last scaled (zero if never).
	DescribeServiceState(ctx context.Context, service string) (serviceState, error)
	// SetDesiredCount updates the desired count and stamps the last-scaled tag to
	// now so the cooldown can be honored on the next tick.
	SetDesiredCount(ctx context.Context, service string, desired int, now time.Time) error
}

// serviceState is the live scaling state of one worker service.
type serviceState struct {
	DesiredCount   int
	PrimaryRunning int
	LastScaled     time.Time
}

// runScale sets each configured service's desired count from its newest
// versioned-queue backlog, honoring the per-service cooldown. A failure scaling
// one service does not abort the others: errors are collected so one stuck
// service cannot freeze the whole fleet's autoscaling.
func runScale(ctx context.Context, fetcher depthFetcher, scaler serviceScaler, scaling map[string]workerlifecycle.ServiceScaling, now time.Time, logger *slog.Logger) error {
	if len(scaling) == 0 {
		return nil
	}
	depths, err := fetchDepths(ctx, fetcher)
	if err != nil {
		return err
	}

	var errs []error
	for _, service := range sortedKeys(scaling) {
		if err := scaleService(ctx, scaler, service, scaling[service], depths, now, logger); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func scaleService(ctx context.Context, scaler serviceScaler, service string, sc workerlifecycle.ServiceScaling, depths map[string]int64, now time.Time, logger *slog.Logger) error {
	state, err := scaler.DescribeServiceState(ctx, service)
	if err != nil {
		// A configured service the cluster does not have (the lambda was enabled
		// before its worker, or a stale config key) is skipped, not an error:
		// erroring would fail the whole tick for every other service.
		if errors.Is(err, errServiceMissing) {
			return nil
		}
		return err
	}
	if sc.CooldownActive(state.LastScaled, now) {
		return nil
	}
	backlog, ok := workerlifecycle.NewestVersionedDepth(depths, sc.QueueBase)
	if !ok && sc.Max > 0 {
		// No versioned queue is present in this snapshot. For an enabled service
		// that means the broker listing transiently dropped the queue (a restart,
		// a partial page), not that the backlog is genuinely zero - so hold the
		// current count rather than scaling a busy fleet down. A disabled service
		// (Max == 0) still falls through and is forced to zero below.
		return nil
	}
	desired := sc.ComputeDesiredCount(state.DesiredCount, backlog)
	if desired == state.DesiredCount {
		return nil
	}
	if err := scaler.SetDesiredCount(ctx, service, desired, now); err != nil {
		return err
	}
	logger.InfoContext(
		ctx, "workerlifecycle: scaled service",
		slog.String("service", service),
		slog.Int("from", state.DesiredCount),
		slog.Int("to", desired),
		slog.Int64("backlog", backlog),
	)
	return nil
}

// fetchDepths collects the broker's per-queue backlog into a name->count map the
// decision logic consumes.
func fetchDepths(ctx context.Context, fetcher depthFetcher) (map[string]int64, error) {
	queues, err := fetcher.FetchQueues(ctx)
	if err != nil {
		return nil, err
	}
	depths := make(map[string]int64, len(queues))
	for _, q := range queues {
		depths[q.Name] = q.Messages
	}
	return depths, nil
}

// sortedKeys returns the service names in a stable order so logs and tests are
// deterministic.
func sortedKeys(scaling map[string]workerlifecycle.ServiceScaling) []string {
	keys := make([]string, 0, len(scaling))
	for k := range scaling {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
