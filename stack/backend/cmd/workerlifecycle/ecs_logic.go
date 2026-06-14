package main

import (
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/verovec/truth-in-stream/backend/internal/workerlifecycle"
)

const (
	// lastScaledTagKey stamps the time a service was last scaled so the cooldown
	// survives across lambda invocations (the lambda is stateless).
	lastScaledTagKey = "worker-lifecycle:last-scaled-at"
	// queueVersionsEnv is the worker container env var carrying the oldest-first
	// version list; its active (newest) entry is the queue version a task set's
	// workers consume.
	queueVersionsEnv = "RABBITMQ_QUEUE_VERSIONS"

	statusPrimary = "PRIMARY"
	statusActive  = "ACTIVE"
)

// lastScaledFromTags returns the time stored in the last-scaled tag, or the zero
// time when the tag is absent or unparseable - both treated as "never scaled" so
// a missing tag never blocks the first scale.
func lastScaledFromTags(tags []ecstypes.Tag) time.Time {
	for _, t := range tags {
		if t.Key != nil && *t.Key == lastScaledTagKey && t.Value != nil {
			if ts, err := time.Parse(time.RFC3339, *t.Value); err == nil {
				return ts
			}
		}
	}
	return time.Time{}
}

// containerQueueVersion reads the active queue version a task set's workers
// consume from the first container env that carries RABBITMQ_QUEUE_VERSIONS,
// returning "" when none is present (an orphan the cleanup logic always retires).
func containerQueueVersion(containers []ecstypes.ContainerDefinition) string {
	for _, c := range containers {
		for _, e := range c.Environment {
			if e.Name != nil && *e.Name == queueVersionsEnv && e.Value != nil {
				if v, ok := workerlifecycle.ActiveVersion(*e.Value); ok {
					return v
				}
			}
		}
	}
	return ""
}

// taskSetNetwork extracts the awsvpc placement of a task set, empty when it
// carries none.
func taskSetNetwork(ts ecstypes.TaskSet) awsvpcNetwork {
	if ts.NetworkConfiguration == nil || ts.NetworkConfiguration.AwsvpcConfiguration == nil {
		return awsvpcNetwork{}
	}
	c := ts.NetworkConfiguration.AwsvpcConfiguration
	return awsvpcNetwork{Subnets: c.Subnets, SecurityGroups: c.SecurityGroups}
}

// registerInput builds the RegisterTaskDefinition request for a new revision of an
// existing task definition with its first container's image replaced, forwarding
// only the fields a Fargate worker definition needs so server-managed fields
// (revision, ARN, status) are not echoed back.
func registerInput(td ecstypes.TaskDefinition, image string) *ecs.RegisterTaskDefinitionInput {
	containers := make([]ecstypes.ContainerDefinition, len(td.ContainerDefinitions))
	copy(containers, td.ContainerDefinitions)
	if len(containers) > 0 {
		containers[0].Image = awssdk.String(image)
	}
	return &ecs.RegisterTaskDefinitionInput{
		Family:                  td.Family,
		ContainerDefinitions:    containers,
		TaskRoleArn:             td.TaskRoleArn,
		ExecutionRoleArn:        td.ExecutionRoleArn,
		NetworkMode:             td.NetworkMode,
		RequiresCompatibilities: td.RequiresCompatibilities,
		Cpu:                     td.Cpu,
		Memory:                  td.Memory,
		Volumes:                 td.Volumes,
		RuntimePlatform:         td.RuntimePlatform,
		EphemeralStorage:        td.EphemeralStorage,
		PlacementConstraints:    td.PlacementConstraints,
	}
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
