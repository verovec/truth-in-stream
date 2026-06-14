package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/verovec/truth-in-stream/backend/internal/workerlifecycle"
)

// ecsAPI is the slice of the ECS client the adapter depends on, kept narrow so
// the orchestration can be tested against a fake while the lambda wires the real
// client.
type ecsAPI interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, optFns ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
	TagResource(ctx context.Context, in *ecs.TagResourceInput, optFns ...func(*ecs.Options)) (*ecs.TagResourceOutput, error)
	DeleteTaskSet(ctx context.Context, in *ecs.DeleteTaskSetInput, optFns ...func(*ecs.Options)) (*ecs.DeleteTaskSetOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, optFns ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	CreateTaskSet(ctx context.Context, in *ecs.CreateTaskSetInput, optFns ...func(*ecs.Options)) (*ecs.CreateTaskSetOutput, error)
	UpdateServicePrimaryTaskSet(ctx context.Context, in *ecs.UpdateServicePrimaryTaskSetInput, optFns ...func(*ecs.Options)) (*ecs.UpdateServicePrimaryTaskSetOutput, error)
}

// errServiceMissing marks a configured service that the cluster does not have, so
// the scheduled handlers can skip it instead of failing the whole tick (the
// lambda may be enabled before its worker, or carry a stale config key).
var errServiceMissing = errors.New("workerlifecycle: service not found")

// ecsAdapter wires the ECS client to the scale/cleanup/deploy orchestration ports.
type ecsAdapter struct {
	ecs     ecsAPI
	cluster string
}

func (a *ecsAdapter) describeService(ctx context.Context, service string, include []ecstypes.ServiceField) (ecstypes.Service, error) {
	out, err := a.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  awssdk.String(a.cluster),
		Services: []string{service},
		Include:  include,
	})
	if err != nil {
		return ecstypes.Service{}, fmt.Errorf("workerlifecycle: describe service %q: %w", service, err)
	}
	if len(out.Services) == 0 {
		// A not-found service comes back in Failures with reason MISSING; surface
		// any other reason verbatim so a misconfiguration is diagnosable.
		reason := "MISSING"
		if len(out.Failures) > 0 && out.Failures[0].Reason != nil {
			reason = *out.Failures[0].Reason
		}
		if reason == "MISSING" {
			return ecstypes.Service{}, fmt.Errorf("%w: %q", errServiceMissing, service)
		}
		return ecstypes.Service{}, fmt.Errorf("workerlifecycle: describe service %q failed: %s", service, reason)
	}
	return out.Services[0], nil
}

// DescribeServiceState implements serviceScaler.
func (a *ecsAdapter) DescribeServiceState(ctx context.Context, service string) (serviceState, error) {
	svc, err := a.describeService(ctx, service, []ecstypes.ServiceField{ecstypes.ServiceFieldTags})
	if err != nil {
		return serviceState{}, err
	}
	state := serviceState{
		DesiredCount: int(svc.DesiredCount),
		LastScaled:   lastScaledFromTags(svc.Tags),
	}
	for _, ts := range svc.TaskSets {
		if ts.Status != nil && *ts.Status == statusPrimary {
			state.PrimaryRunning = int(ts.RunningCount)
		}
	}
	return state, nil
}

// SetDesiredCount implements serviceScaler.
func (a *ecsAdapter) SetDesiredCount(ctx context.Context, service string, desired int, now time.Time) error {
	if _, err := a.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      awssdk.String(a.cluster),
		Service:      awssdk.String(service),
		DesiredCount: awssdk.Int32(int32(desired)),
	}); err != nil {
		return fmt.Errorf("workerlifecycle: update desired count for %q: %w", service, err)
	}
	svc, err := a.describeService(ctx, service, nil)
	if err != nil {
		return err
	}
	if svc.ServiceArn == nil {
		return fmt.Errorf("workerlifecycle: service %q has no ARN to tag", service)
	}
	if _, err := a.ecs.TagResource(ctx, &ecs.TagResourceInput{
		ResourceArn: svc.ServiceArn,
		Tags: []ecstypes.Tag{{
			Key:   awssdk.String(lastScaledTagKey),
			Value: awssdk.String(now.UTC().Format(time.RFC3339)),
		}},
	}); err != nil {
		return fmt.Errorf("workerlifecycle: tag last-scaled for %q: %w", service, err)
	}
	return nil
}

// DescribeCleanupState implements taskSetManager.
func (a *ecsAdapter) DescribeCleanupState(ctx context.Context, service string) (cleanupState, error) {
	svc, err := a.describeService(ctx, service, nil)
	if err != nil {
		return cleanupState{}, err
	}
	state := cleanupState{DesiredCount: int(svc.DesiredCount)}
	for _, ts := range svc.TaskSets {
		if ts.Status == nil || ts.Id == nil {
			continue
		}
		version, err := a.taskSetVersion(ctx, ts)
		if err != nil {
			return cleanupState{}, err
		}
		switch *ts.Status {
		case statusPrimary:
			state.HasPrimary = true
			state.Primary = workerlifecycle.PrimaryTaskSet{
				Version:      version,
				CreatedAt:    deref(ts.CreatedAt),
				RunningCount: int(ts.RunningCount),
			}
		case statusActive:
			state.NonPrimary = append(state.NonPrimary, workerlifecycle.TaskSet{
				ID:           *ts.Id,
				Version:      version,
				CreatedAt:    deref(ts.CreatedAt),
				RunningCount: int(ts.RunningCount),
			})
		}
	}
	return state, nil
}

func (a *ecsAdapter) taskSetVersion(ctx context.Context, ts ecstypes.TaskSet) (string, error) {
	if ts.TaskDefinition == nil {
		return "", nil
	}
	out, err := a.ecs.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: ts.TaskDefinition,
	})
	if err != nil {
		return "", fmt.Errorf("workerlifecycle: describe task definition %q: %w", *ts.TaskDefinition, err)
	}
	if out.TaskDefinition == nil {
		return "", nil
	}
	return containerQueueVersion(out.TaskDefinition.ContainerDefinitions), nil
}

// DeleteTaskSet implements taskSetManager.
func (a *ecsAdapter) DeleteTaskSet(ctx context.Context, service, taskSetID string) error {
	if _, err := a.ecs.DeleteTaskSet(ctx, &ecs.DeleteTaskSetInput{
		Cluster: awssdk.String(a.cluster),
		Service: awssdk.String(service),
		TaskSet: awssdk.String(taskSetID),
		Force:   awssdk.Bool(true),
	}); err != nil {
		return fmt.Errorf("workerlifecycle: delete task set %q: %w", taskSetID, err)
	}
	return nil
}

// PrimaryNetwork implements deployer.
func (a *ecsAdapter) PrimaryNetwork(ctx context.Context, service string) (awsvpcNetwork, bool, error) {
	svc, err := a.describeService(ctx, service, nil)
	if err != nil {
		return awsvpcNetwork{}, false, err
	}
	for _, ts := range svc.TaskSets {
		if ts.Status != nil && *ts.Status == statusPrimary {
			return taskSetNetwork(ts), true, nil
		}
	}
	return awsvpcNetwork{}, false, nil
}

// RegisterImageRevision implements deployer.
func (a *ecsAdapter) RegisterImageRevision(ctx context.Context, family, image string) (string, error) {
	out, err := a.ecs.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
		TaskDefinition: awssdk.String(family),
	})
	if err != nil {
		return "", fmt.Errorf("workerlifecycle: describe task definition %q: %w", family, err)
	}
	if out.TaskDefinition == nil {
		return "", fmt.Errorf("workerlifecycle: task definition %q not found", family)
	}
	registered, err := a.ecs.RegisterTaskDefinition(ctx, registerInput(*out.TaskDefinition, image))
	if err != nil {
		return "", fmt.Errorf("workerlifecycle: register task definition %q: %w", family, err)
	}
	if registered.TaskDefinition == nil || registered.TaskDefinition.TaskDefinitionArn == nil {
		return "", errors.New("workerlifecycle: registered task definition has no ARN")
	}
	return *registered.TaskDefinition.TaskDefinitionArn, nil
}

// CreateTaskSet implements deployer.
func (a *ecsAdapter) CreateTaskSet(ctx context.Context, service, taskDefinition string, network awsvpcNetwork) (string, error) {
	out, err := a.ecs.CreateTaskSet(ctx, &ecs.CreateTaskSetInput{
		Cluster:        awssdk.String(a.cluster),
		Service:        awssdk.String(service),
		TaskDefinition: awssdk.String(taskDefinition),
		LaunchType:     ecstypes.LaunchTypeFargate,
		Scale:          &ecstypes.Scale{Unit: ecstypes.ScaleUnitPercent, Value: 100},
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        network.Subnets,
				SecurityGroups: network.SecurityGroups,
				AssignPublicIp: ecstypes.AssignPublicIpDisabled,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("workerlifecycle: create task set for %q: %w", service, err)
	}
	if out.TaskSet == nil || out.TaskSet.Id == nil {
		return "", errors.New("workerlifecycle: created task set has no id")
	}
	return *out.TaskSet.Id, nil
}

// PromoteTaskSet implements deployer.
func (a *ecsAdapter) PromoteTaskSet(ctx context.Context, service, taskSetID string) error {
	if _, err := a.ecs.UpdateServicePrimaryTaskSet(ctx, &ecs.UpdateServicePrimaryTaskSetInput{
		Cluster:        awssdk.String(a.cluster),
		Service:        awssdk.String(service),
		PrimaryTaskSet: awssdk.String(taskSetID),
	}); err != nil {
		return fmt.Errorf("workerlifecycle: promote task set %q: %w", taskSetID, err)
	}
	return nil
}
