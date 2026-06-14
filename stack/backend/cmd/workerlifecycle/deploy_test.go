package main

import (
	"context"
	"testing"
)

type fakeDeployer struct {
	network       awsvpcNetwork
	hasPrimary    bool
	registered    string
	createdNet    awsvpcNetwork
	createdTaskID string
	promoted      []string
}

func (f *fakeDeployer) PrimaryNetwork(context.Context, string) (awsvpcNetwork, bool, error) {
	return f.network, f.hasPrimary, nil
}

func (f *fakeDeployer) RegisterImageRevision(_ context.Context, _, _ string) (string, error) {
	return f.registered, nil
}

func (f *fakeDeployer) CreateTaskSet(_ context.Context, _, _ string, network awsvpcNetwork) (string, error) {
	f.createdNet = network
	return f.createdTaskID, nil
}

func (f *fakeDeployer) PromoteTaskSet(_ context.Context, _, taskSetID string) error {
	f.promoted = append(f.promoted, taskSetID)
	return nil
}

func TestRunDeploy(t *testing.T) {
	t.Parallel()
	boot := bootstrapConfig{
		resourcePrefix: "truth-in-stream-dev",
		network:        awsvpcNetwork{Subnets: []string{"subnet-a"}, SecurityGroups: []string{"sg-a"}},
	}

	t.Run("bootstrap uses configured network and promotes", func(t *testing.T) {
		t.Parallel()
		d := &fakeDeployer{hasPrimary: false, registered: "arn:td:2", createdTaskID: "ts-1"}
		event := deployEvent{Image: "repo:tag", Services: []string{"embedworker"}}
		if err := runDeploy(context.Background(), d, event, boot, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(d.promoted) != 1 || d.promoted[0] != "ts-1" {
			t.Fatalf("expected to promote ts-1, got %v", d.promoted)
		}
		if len(d.createdNet.Subnets) != 1 || d.createdNet.Subnets[0] != "subnet-a" {
			t.Fatalf("expected bootstrap network, got %v", d.createdNet)
		}
	})

	t.Run("existing primary network is reused", func(t *testing.T) {
		t.Parallel()
		d := &fakeDeployer{
			hasPrimary:    true,
			network:       awsvpcNetwork{Subnets: []string{"subnet-live"}, SecurityGroups: []string{"sg-live"}},
			registered:    "arn:td:3",
			createdTaskID: "ts-2",
		}
		event := deployEvent{Image: "repo:tag", Services: []string{"embedworker"}}
		if err := runDeploy(context.Background(), d, event, boot, discardLogger()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.createdNet.Subnets[0] != "subnet-live" {
			t.Fatalf("expected to reuse primary network, got %v", d.createdNet)
		}
	})

	t.Run("bootstrap without a network fails", func(t *testing.T) {
		t.Parallel()
		d := &fakeDeployer{hasPrimary: false, registered: "arn:td:2", createdTaskID: "ts-1"}
		event := deployEvent{Image: "repo:tag", Services: []string{"embedworker"}}
		err := runDeploy(context.Background(), d, event, bootstrapConfig{resourcePrefix: "p"}, discardLogger())
		if err == nil {
			t.Fatal("expected error when no network is available")
		}
		if len(d.promoted) != 0 {
			t.Fatalf("expected no promotion on failure, got %v", d.promoted)
		}
	})

	t.Run("missing image is rejected", func(t *testing.T) {
		t.Parallel()
		d := &fakeDeployer{}
		if err := runDeploy(context.Background(), d, deployEvent{Services: []string{"x"}}, boot, discardLogger()); err == nil {
			t.Fatal("expected error for empty image")
		}
	})

	t.Run("missing services is rejected", func(t *testing.T) {
		t.Parallel()
		d := &fakeDeployer{}
		if err := runDeploy(context.Background(), d, deployEvent{Image: "repo:tag"}, boot, discardLogger()); err == nil {
			t.Fatal("expected error for empty services")
		}
	})
}

func TestBootstrapFamily(t *testing.T) {
	t.Parallel()
	boot := bootstrapConfig{resourcePrefix: "truth-in-stream-dev"}
	if got := boot.family("embedworker"); got != "truth-in-stream-dev-embedworker" {
		t.Fatalf("family = %q, want truth-in-stream-dev-embedworker", got)
	}
}
