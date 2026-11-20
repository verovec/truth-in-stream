// Command mqmetrics is an AWS Lambda that polls the Amazon MQ for RabbitMQ
// management API and republishes per-queue backlog, consumer count and publish
// rate as custom CloudWatch metrics, plus a version-stripped rollup so a
// dashboard can reference a stable queue name as versions roll. It runs on an
// EventBridge schedule because Amazon MQ exposes no per-queue metrics natively.
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
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/verovec/truth-in-stream/backend/internal/mqmetrics"
)

// maxDatumsPerCall is CloudWatch's PutMetricData ceiling (1000 MetricDatum per
// request).
const maxDatumsPerCall = 1000

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("mqmetrics: startup failed", "error", err)
		os.Exit(1)
	}
}

// run loads configuration and the AWS clients once (a warm lambda container
// reuses them across invocations) and starts the handler loop.
func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("mqmetrics: load aws config: %w", err)
	}
	secrets := secretsmanager.NewFromConfig(awsCfg)
	cw := cloudwatch.NewFromConfig(awsCfg)
	httpClient := &http.Client{Timeout: cfg.httpTimeout}

	lambda.Start(func(ctx context.Context) error {
		secret, err := fetchSecret(ctx, secrets, cfg.secretARN)
		if err != nil {
			return err
		}
		creds, err := mqmetrics.ParseBrokerURL(secret)
		if err != nil {
			return err
		}
		client := &mqmetrics.Client{
			HTTP:    httpClient,
			BaseURL: fmt.Sprintf("https://%s:%d", creds.Host, cfg.managementPort),
			Creds:   creds,
		}
		return poll(ctx, client, cw, mqmetrics.Options{Broker: cfg.brokerName, Bases: cfg.queueNames}, cfg.namespace)
	})
	return nil
}

type config struct {
	secretARN      string
	namespace      string
	brokerName     string
	queueNames     []string
	managementPort int
	httpTimeout    time.Duration
}

func loadConfig() (config, error) {
	secretARN := os.Getenv("RABBITMQ_URL_SECRET_ARN")
	if secretARN == "" {
		return config{}, errors.New("mqmetrics: RABBITMQ_URL_SECRET_ARN is required")
	}
	brokerName := os.Getenv("BROKER_NAME")
	if brokerName == "" {
		return config{}, errors.New("mqmetrics: BROKER_NAME is required")
	}
	port, err := intEnv("MANAGEMENT_PORT", 443)
	if err != nil {
		return config{}, err
	}
	timeoutSeconds, err := intEnv("HTTP_TIMEOUT_SECONDS", 10)
	if err != nil {
		return config{}, err
	}
	return config{
		secretARN:      secretARN,
		namespace:      getenv("METRICS_NAMESPACE", "TruthInStream/RabbitMQ"),
		brokerName:     brokerName,
		queueNames:     queueNames(getenv("QUEUE_NAMES", "embedding.jobs")),
		managementPort: port,
		httpTimeout:    time.Duration(timeoutSeconds) * time.Second,
	}, nil
}

// queueNames parses the comma-separated QUEUE_NAMES list into base queue names,
// trimming blanks. Each base's versioned and dead-letter queues are measured.
func queueNames(raw string) []string {
	parts := strings.Split(raw, ",")
	bases := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			bases = append(bases, v)
		}
	}
	return bases
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("mqmetrics: %s must be an integer: %w", key, err)
	}
	return v, nil
}

// queueFetcher is the slice of the management-API client poll depends on, so a
// test can drive it without a live broker.
type queueFetcher interface {
	FetchQueues(ctx context.Context) ([]mqmetrics.APIQueue, error)
}

// metricPutter is the slice of the CloudWatch client publish depends on.
type metricPutter interface {
	PutMetricData(ctx context.Context, in *cloudwatch.PutMetricDataInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error)
}

type secretGetter interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

func fetchSecret(ctx context.Context, getter secretGetter, arn string) (string, error) {
	out, err := getter.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: awssdk.String(arn)})
	if err != nil {
		return "", fmt.Errorf("mqmetrics: get secret: %w", err)
	}
	if out.SecretString == nil {
		return "", errors.New("mqmetrics: secret has no string value")
	}
	return *out.SecretString, nil
}

func poll(ctx context.Context, fetcher queueFetcher, putter metricPutter, opts mqmetrics.Options, namespace string) error {
	queues, err := fetcher.FetchQueues(ctx)
	if err != nil {
		return err
	}
	return publish(ctx, putter, namespace, mqmetrics.ShapeMetrics(queues, opts))
}

func publish(ctx context.Context, putter metricPutter, namespace string, datums []mqmetrics.Datum) error {
	for _, batch := range mqmetrics.BatchDatums(datums, maxDatumsPerCall) {
		if _, err := putter.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace:  awssdk.String(namespace),
			MetricData: toMetricData(batch),
		}); err != nil {
			return fmt.Errorf("mqmetrics: put metric data: %w", err)
		}
	}
	return nil
}

func toMetricData(datums []mqmetrics.Datum) []cwtypes.MetricDatum {
	out := make([]cwtypes.MetricDatum, len(datums))
	for i, d := range datums {
		dims := make([]cwtypes.Dimension, len(d.Dimensions))
		for j, dim := range d.Dimensions {
			dims[j] = cwtypes.Dimension{Name: awssdk.String(dim.Name), Value: awssdk.String(dim.Value)}
		}
		out[i] = cwtypes.MetricDatum{
			MetricName: awssdk.String(d.MetricName),
			Dimensions: dims,
			Value:      awssdk.Float64(d.Value),
			Unit:       cwtypes.StandardUnit(d.Unit),
		}
	}
	return out
}
