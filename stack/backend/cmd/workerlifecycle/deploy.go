package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// deployEvent is the payload the deploy workflow sends: the image to roll out and
// the worker services to roll it to.
type deployEvent struct {
	Image    string   `json:"image"`
	Services []string `json:"services"`
}

// awsvpcNetwork is the subnet/security-group placement a new task set runs in.
type awsvpcNetwork struct {
	Subnets        []string
	SecurityGroups []string
}

func (n awsvpcNetwork) empty() bool {
	return len(n.Subnets) == 0 || len(n.SecurityGroups) == 0
}

// bootstrapConfig provides the family-name derivation and the network placement a
// first task set needs when the service has no PRIMARY to copy from.
type bootstrapConfig struct {
	resourcePrefix string
	network        awsvpcNetwork
}

// family returns the task-definition family for a worker service, matching the
// terraform worker module's naming (<project>-<environment>-<service>), so the
// deploy handler resolves the base task definition without being handed it.
func (b bootstrapConfig) family(service string) string {
	return b.resourcePrefix + "-" + service
}

// deployer creates and promotes ECS task sets for a service under the EXTERNAL
// deployment controller.
type deployer interface {
	// PrimaryNetwork returns the network placement of the service's PRIMARY task
	// set, or HasPrimary false when none is serving yet (the first deploy).
	PrimaryNetwork(ctx context.Context, service string) (network awsvpcNetwork, hasPrimary bool, err error)
	// RegisterImageRevision registers a new revision of family with its first
	// container's image replaced, returning the new task-definition ARN.
	RegisterImageRevision(ctx context.Context, family, image string) (string, error)
	// CreateTaskSet creates a new task set at 100% scale on the given network.
	CreateTaskSet(ctx context.Context, service, taskDefinition string, network awsvpcNetwork) (string, error)
	// PromoteTaskSet makes the task set the service's PRIMARY.
	PromoteTaskSet(ctx context.Context, service, taskSetID string) error
}

// runDeploy rolls each requested service to the new image by registering a new
// task-definition revision, creating a task set on the service's network (the
// PRIMARY's when one exists, the configured bootstrap network on the first
// deploy), and promoting it to PRIMARY. The old PRIMARY becomes a non-PRIMARY task
// set that the cleanup handler retires once its version's queues drain, so the
// roll never drops in-flight work.
func runDeploy(ctx context.Context, d deployer, event deployEvent, boot bootstrapConfig, logger *slog.Logger) error {
	if event.Image == "" {
		return errors.New("workerlifecycle: deploy event has no image")
	}
	if len(event.Services) == 0 {
		return errors.New("workerlifecycle: deploy event has no services")
	}
	var errs []error
	for _, service := range event.Services {
		if err := deployService(ctx, d, service, event.Image, boot, logger); err != nil {
			errs = append(errs, fmt.Errorf("workerlifecycle: deploy %s: %w", service, err))
		}
	}
	return errors.Join(errs...)
}

func deployService(ctx context.Context, d deployer, service, image string, boot bootstrapConfig, logger *slog.Logger) error {
	network, hasPrimary, err := d.PrimaryNetwork(ctx, service)
	if err != nil {
		return err
	}
	if !hasPrimary {
		network = boot.network
	}
	if network.empty() {
		return errors.New("no PRIMARY network to copy and no bootstrap network configured")
	}

	taskDef, err := d.RegisterImageRevision(ctx, boot.family(service), image)
	if err != nil {
		return err
	}
	taskSetID, err := d.CreateTaskSet(ctx, service, taskDef, network)
	if err != nil {
		return err
	}
	if err := d.PromoteTaskSet(ctx, service, taskSetID); err != nil {
		return err
	}
	logger.InfoContext(
		ctx, "workerlifecycle: deployed task set",
		slog.String("service", service),
		slog.String("task_set", taskSetID),
		slog.String("task_definition", taskDef),
		slog.Bool("bootstrap", !hasPrimary),
	)
	return nil
}
