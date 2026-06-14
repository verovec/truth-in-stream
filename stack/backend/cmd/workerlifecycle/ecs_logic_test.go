package main

import (
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func TestLastScaledFromTags(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 6, 14, 11, 30, 0, 0, time.UTC)

	t.Run("parses the tag", func(t *testing.T) {
		t.Parallel()
		tags := []ecstypes.Tag{
			{Key: awssdk.String("other"), Value: awssdk.String("x")},
			{Key: awssdk.String(lastScaledTagKey), Value: awssdk.String(ts.Format(time.RFC3339))},
		}
		if got := lastScaledFromTags(tags); !got.Equal(ts) {
			t.Fatalf("lastScaledFromTags = %v, want %v", got, ts)
		}
	})

	t.Run("absent tag is zero time", func(t *testing.T) {
		t.Parallel()
		if got := lastScaledFromTags(nil); !got.IsZero() {
			t.Fatalf("expected zero time, got %v", got)
		}
	})

	t.Run("unparseable value is zero time", func(t *testing.T) {
		t.Parallel()
		tags := []ecstypes.Tag{{Key: awssdk.String(lastScaledTagKey), Value: awssdk.String("not-a-time")}}
		if got := lastScaledFromTags(tags); !got.IsZero() {
			t.Fatalf("expected zero time, got %v", got)
		}
	})
}

func TestContainerQueueVersion(t *testing.T) {
	t.Parallel()
	t.Run("reads active version from env", func(t *testing.T) {
		t.Parallel()
		containers := []ecstypes.ContainerDefinition{{
			Environment: []ecstypes.KeyValuePair{
				{Name: awssdk.String("OTHER"), Value: awssdk.String("x")},
				{Name: awssdk.String(queueVersionsEnv), Value: awssdk.String("1,2,3")},
			},
		}}
		if got := containerQueueVersion(containers); got != "3" {
			t.Fatalf("containerQueueVersion = %q, want 3", got)
		}
	})

	t.Run("no env yields empty", func(t *testing.T) {
		t.Parallel()
		containers := []ecstypes.ContainerDefinition{{Environment: nil}}
		if got := containerQueueVersion(containers); got != "" {
			t.Fatalf("expected empty version, got %q", got)
		}
	})
}

func TestTaskSetNetwork(t *testing.T) {
	t.Parallel()
	t.Run("extracts awsvpc config", func(t *testing.T) {
		t.Parallel()
		ts := ecstypes.TaskSet{NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        []string{"subnet-a"},
				SecurityGroups: []string{"sg-a"},
			},
		}}
		got := taskSetNetwork(ts)
		if len(got.Subnets) != 1 || got.Subnets[0] != "subnet-a" || got.SecurityGroups[0] != "sg-a" {
			t.Fatalf("taskSetNetwork = %+v", got)
		}
	})

	t.Run("nil network is empty", func(t *testing.T) {
		t.Parallel()
		if got := taskSetNetwork(ecstypes.TaskSet{}); !got.empty() {
			t.Fatalf("expected empty network, got %+v", got)
		}
	})
}

func TestRegisterInput(t *testing.T) {
	t.Parallel()
	td := ecstypes.TaskDefinition{
		Family: awssdk.String("truth-in-stream-dev-embedworker"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:  awssdk.String("embedworker"),
			Image: awssdk.String("repo:old"),
		}},
		Cpu:    awssdk.String("256"),
		Memory: awssdk.String("512"),
	}
	in := registerInput(td, "repo:new")
	if in.Family == nil || *in.Family != "truth-in-stream-dev-embedworker" {
		t.Fatalf("family not carried: %v", in.Family)
	}
	if in.ContainerDefinitions[0].Image == nil || *in.ContainerDefinitions[0].Image != "repo:new" {
		t.Fatalf("image not swapped: %v", in.ContainerDefinitions[0].Image)
	}
	// The source task definition must be left untouched (defensive copy).
	if *td.ContainerDefinitions[0].Image != "repo:old" {
		t.Fatalf("source task definition mutated: %v", *td.ContainerDefinitions[0].Image)
	}
	if in.Cpu == nil || *in.Cpu != "256" {
		t.Fatalf("cpu not carried: %v", in.Cpu)
	}
}
