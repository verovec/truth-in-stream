package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/mqmetrics"
	"github.com/verovec/truth-in-stream/backend/internal/workerlifecycle"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeFetcher struct {
	queues []mqmetrics.APIQueue
	err    error
}

func (f fakeFetcher) FetchQueues(context.Context) ([]mqmetrics.APIQueue, error) {
	return f.queues, f.err
}

type setCall struct {
	service string
	desired int
}

type fakeScaler struct {
	state    serviceState
	stateErr error
	setErr   error
	calls    []setCall
}

func (f *fakeScaler) DescribeServiceState(context.Context, string) (serviceState, error) {
	return f.state, f.stateErr
}

func (f *fakeScaler) SetDesiredCount(_ context.Context, service string, desired int, _ time.Time) error {
	f.calls = append(f.calls, setCall{service: service, desired: desired})
	return f.setErr
}

func TestRunScale(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	scaling := map[string]workerlifecycle.ServiceScaling{
		"embedworker": {QueueBase: "embedding.jobs", Ratio: 100, Min: 0, Max: 100, Cooldown: 3 * time.Minute},
	}
	queues := []mqmetrics.APIQueue{{Name: "embedding.jobs.v2", Messages: 9000}}

	t.Run("scales up by one step and stamps", func(t *testing.T) {
		t.Parallel()
		scaler := &fakeScaler{state: serviceState{DesiredCount: 1}}
		err := runScale(context.Background(), fakeFetcher{queues: queues}, scaler, scaling, now, discardLogger())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(scaler.calls) != 1 || scaler.calls[0].desired != 2 {
			t.Fatalf("expected one SetDesiredCount(2), got %v", scaler.calls)
		}
	})

	t.Run("cooldown skips scaling", func(t *testing.T) {
		t.Parallel()
		scaler := &fakeScaler{state: serviceState{DesiredCount: 1, LastScaled: now.Add(-1 * time.Minute)}}
		if err := runScale(context.Background(), fakeFetcher{queues: queues}, scaler, scaling, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(scaler.calls) != 0 {
			t.Fatalf("expected no scaling during cooldown, got %v", scaler.calls)
		}
	})

	t.Run("no change does not call set", func(t *testing.T) {
		t.Parallel()
		steady := []mqmetrics.APIQueue{{Name: "embedding.jobs.v2", Messages: 500}}
		scaler := &fakeScaler{state: serviceState{DesiredCount: 5}}
		if err := runScale(context.Background(), fakeFetcher{queues: steady}, scaler, scaling, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(scaler.calls) != 0 {
			t.Fatalf("expected no scaling at steady state, got %v", scaler.calls)
		}
	})

	t.Run("empty config is a no-op even if fetch would fail", func(t *testing.T) {
		t.Parallel()
		scaler := &fakeScaler{}
		if err := runScale(context.Background(), fakeFetcher{err: errors.New("broker down")}, scaler, nil, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("fetch error propagates", func(t *testing.T) {
		t.Parallel()
		scaler := &fakeScaler{}
		if err := runScale(context.Background(), fakeFetcher{err: errors.New("broker down")}, scaler, scaling, now, discardLogger()); err == nil {
			t.Fatal("expected error when broker fetch fails")
		}
	})

	t.Run("missing service is skipped, not an error", func(t *testing.T) {
		t.Parallel()
		scaler := &fakeScaler{stateErr: errServiceMissing}
		if err := runScale(context.Background(), fakeFetcher{queues: queues}, scaler, scaling, now, discardLogger()); err != nil {
			t.Fatalf("expected missing service to be skipped, got %v", err)
		}
		if len(scaler.calls) != 0 {
			t.Fatalf("expected no scaling for a missing service, got %v", scaler.calls)
		}
	})

	t.Run("transiently absent queue holds an enabled fleet", func(t *testing.T) {
		t.Parallel()
		// No embedding.jobs.vN in the snapshot -> NewestVersionedDepth ok=false.
		scaler := &fakeScaler{state: serviceState{DesiredCount: 6}}
		empty := []mqmetrics.APIQueue{{Name: "other.v1", Messages: 0}}
		if err := runScale(context.Background(), fakeFetcher{queues: empty}, scaler, scaling, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(scaler.calls) != 0 {
			t.Fatalf("expected to hold the fleet when the queue is absent, got %v", scaler.calls)
		}
	})

	t.Run("max zero forces desired to zero", func(t *testing.T) {
		t.Parallel()
		disabled := map[string]workerlifecycle.ServiceScaling{
			"embedworker": {QueueBase: "embedding.jobs", Ratio: 100, Min: 0, Max: 0},
		}
		scaler := &fakeScaler{state: serviceState{DesiredCount: 3}}
		if err := runScale(context.Background(), fakeFetcher{queues: queues}, scaler, disabled, now, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(scaler.calls) != 1 || scaler.calls[0].desired != 0 {
			t.Fatalf("expected SetDesiredCount(0) for disabled service, got %v", scaler.calls)
		}
	})
}
