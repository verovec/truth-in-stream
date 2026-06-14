// Command workerlifecycle is an AWS Lambda that runs the embedding-worker fleet's
// lifecycle on the ECS cluster. One binary, three handlers selected by the
// LIFECYCLE_HANDLER env var so a single zip backs three lambda functions:
//
//   - scale: read each worker service's newest versioned-queue backlog and set its
//     desired replica count within configured bounds, on an EventBridge schedule.
//   - cleanup: retire old-version task sets once their queues have drained, on an
//     EventBridge schedule.
//   - deploy: roll a service to a new image by creating and promoting a new task
//     set, invoked by the deploy workflow.
//
// The service runs under an EXTERNAL deployment controller, so this lambda owns
// desired count and task-set rollout; terraform owns only the service shell. All
// scaling/rollout decision logic lives in internal/workerlifecycle and is unit
// tested; this command is the wiring that drives it from ECS, Parameter Store and
// the RabbitMQ management API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/verovec/truth-in-stream/backend/internal/mqmetrics"
	"github.com/verovec/truth-in-stream/backend/internal/workerlifecycle"
)

const (
	handlerScale   = "scale"
	handlerCleanup = "cleanup"
	handlerDeploy  = "deploy"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(context.Background(), logger); err != nil {
		logger.Error("workerlifecycle: startup failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run loads configuration and the AWS clients once (a warm lambda container
// reuses them across invocations) and starts the handler selected by
// LIFECYCLE_HANDLER.
func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("workerlifecycle: load aws config: %w", err)
	}
	adapter := &ecsAdapter{
		ecs:     ecs.NewFromConfig(awsCfg),
		cluster: cfg.cluster,
	}

	switch cfg.handler {
	case handlerScale:
		broker, err := newBrokerClient(ctx, awsCfg, cfg)
		if err != nil {
			return err
		}
		scaling, err := loadScalingConfig(ctx, ssm.NewFromConfig(awsCfg), cfg.scalingConfigParam)
		if err != nil {
			return err
		}
		lambda.Start(func(ctx context.Context) error {
			return runScale(ctx, broker, adapter, scaling, time.Now(), logger)
		})
	case handlerCleanup:
		broker, err := newBrokerClient(ctx, awsCfg, cfg)
		if err != nil {
			return err
		}
		scaling, err := loadScalingConfig(ctx, ssm.NewFromConfig(awsCfg), cfg.scalingConfigParam)
		if err != nil {
			return err
		}
		lambda.Start(func(ctx context.Context) error {
			return runCleanup(ctx, broker, adapter, scaling, cfg.retirePolicy(), time.Now(), logger)
		})
	case handlerDeploy:
		lambda.Start(func(ctx context.Context, event deployEvent) error {
			return runDeploy(ctx, adapter, event, cfg.bootstrap(), logger)
		})
	default:
		return fmt.Errorf("workerlifecycle: unknown LIFECYCLE_HANDLER %q", cfg.handler)
	}
	return nil
}

// newBrokerClient builds the RabbitMQ management-API client from the broker URL
// secret, reusing the mqmetrics adapter so both lambdas share one parser.
func newBrokerClient(ctx context.Context, awsCfg awssdk.Config, cfg config) (*mqmetrics.Client, error) {
	secrets := secretsmanager.NewFromConfig(awsCfg)
	out, err := secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: awssdk.String(cfg.brokerSecretARN)})
	if err != nil {
		return nil, fmt.Errorf("workerlifecycle: get broker secret: %w", err)
	}
	if out.SecretString == nil {
		return nil, errors.New("workerlifecycle: broker secret has no string value")
	}
	creds, err := mqmetrics.ParseBrokerURL(*out.SecretString)
	if err != nil {
		return nil, err
	}
	return &mqmetrics.Client{
		HTTP:    &http.Client{Timeout: cfg.httpTimeout},
		BaseURL: fmt.Sprintf("https://%s:%d", creds.Host, cfg.managementPort),
		Creds:   creds,
	}, nil
}

// loadScalingConfig reads the per-service scaling config JSON from Parameter Store
// and decodes it. The config lives in Parameter Store because the full per-service
// map can exceed the lambda's 4 KiB env-var limit.
func loadScalingConfig(ctx context.Context, client ssmGetter, name string) (map[string]workerlifecycle.ServiceScaling, error) {
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: awssdk.String(name)})
	if err != nil {
		return nil, fmt.Errorf("workerlifecycle: read scaling config %q: %w", name, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return map[string]workerlifecycle.ServiceScaling{}, nil
	}
	return workerlifecycle.ParseScalingConfig([]byte(*out.Parameter.Value))
}

type ssmGetter interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

type config struct {
	handler            string
	cluster            string
	scalingConfigParam string
	brokerSecretARN    string
	managementPort     int
	httpTimeout        time.Duration
	maxAge             time.Duration
	sameVersionMinAge  time.Duration
	zombieMinAge       time.Duration
	resourcePrefix     string
	taskSubnets        []string
	taskSecurityGroups []string
}

func (c config) retirePolicy() workerlifecycle.RetirePolicy {
	return workerlifecycle.RetirePolicy{
		MaxAge:            c.maxAge,
		SameVersionMinAge: c.sameVersionMinAge,
		ZombieMinAge:      c.zombieMinAge,
	}
}

func (c config) bootstrap() bootstrapConfig {
	return bootstrapConfig{
		resourcePrefix: c.resourcePrefix,
		network:        awsvpcNetwork{Subnets: c.taskSubnets, SecurityGroups: c.taskSecurityGroups},
	}
}

func loadConfig() (config, error) {
	handler := os.Getenv("LIFECYCLE_HANDLER")
	if handler == "" {
		return config{}, errors.New("workerlifecycle: LIFECYCLE_HANDLER is required")
	}
	cluster := os.Getenv("ECS_CLUSTER")
	if cluster == "" {
		return config{}, errors.New("workerlifecycle: ECS_CLUSTER is required")
	}
	port, err := intEnv("QUEUE_MANAGEMENT_PORT", 443)
	if err != nil {
		return config{}, err
	}
	timeoutSeconds, err := intEnv("HTTP_TIMEOUT_SECONDS", 10)
	if err != nil {
		return config{}, err
	}
	maxAgeHours, err := intEnv("MAX_AGE_HOURS", 24)
	if err != nil {
		return config{}, err
	}
	sameVersionMinutes, err := intEnv("SAME_VERSION_MIN_AGE_MINUTES", 2)
	if err != nil {
		return config{}, err
	}
	zombieMinutes, err := intEnv("ZOMBIE_MIN_AGE_MINUTES", 15)
	if err != nil {
		return config{}, err
	}

	cfg := config{
		handler:            handler,
		cluster:            cluster,
		scalingConfigParam: os.Getenv("SCALING_CONFIG_PARAM"),
		brokerSecretARN:    os.Getenv("RABBITMQ_URL_SECRET_ARN"),
		managementPort:     port,
		httpTimeout:        time.Duration(timeoutSeconds) * time.Second,
		maxAge:             time.Duration(maxAgeHours) * time.Hour,
		sameVersionMinAge:  time.Duration(sameVersionMinutes) * time.Minute,
		zombieMinAge:       time.Duration(zombieMinutes) * time.Minute,
		resourcePrefix:     os.Getenv("RESOURCE_PREFIX"),
		taskSubnets:        splitEnv("TASK_SUBNET_IDS"),
		taskSecurityGroups: splitEnv("TASK_SECURITY_GROUP_IDS"),
	}

	switch handler {
	case handlerScale, handlerCleanup:
		if cfg.scalingConfigParam == "" {
			return config{}, fmt.Errorf("workerlifecycle: SCALING_CONFIG_PARAM is required for the %s handler", handler)
		}
		if cfg.brokerSecretARN == "" {
			return config{}, fmt.Errorf("workerlifecycle: RABBITMQ_URL_SECRET_ARN is required for the %s handler", handler)
		}
	case handlerDeploy:
		if cfg.resourcePrefix == "" {
			return config{}, errors.New("workerlifecycle: RESOURCE_PREFIX is required for the deploy handler")
		}
	default:
		return config{}, fmt.Errorf("workerlifecycle: unknown LIFECYCLE_HANDLER %q", handler)
	}
	return cfg, nil
}

func intEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("workerlifecycle: %s must be an integer: %w", key, err)
	}
	return v, nil
}

// splitEnv parses a comma-separated env var into a trimmed, non-empty slice.
func splitEnv(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
