package crawlnotify

import (
	"context"
	"fmt"
	"log/slog"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// Run-outcome metric names and dimensions. A finished (successful) run publishes
// MetricRunSuccess = 1 dimensioned by the source, so a "no successful run in N
// hours" CloudWatch alarm can page per source. The metric is deliberately
// success-only: a failed run publishes nothing here (its own RunFailed Slack alert
// covers it), so the alarm keys purely on the absence of successes.
const (
	MetricRunSuccess = "RunSuccess"
	DimensionSource  = "Source"
)

// metricPutter is the CloudWatch surface the run-metric notifier needs; the
// concrete *cloudwatch.Client satisfies it, and a test fake records the calls.
type metricPutter interface {
	PutMetricData(ctx context.Context, in *cloudwatch.PutMetricDataInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error)
}

// MetricNotifier is a best-effort Notifier that publishes a per-source RunSuccess
// metric to CloudWatch when a run finishes. Like every Notifier a PutMetricData
// failure is swallowed - metrics must never decide a run's outcome.
type MetricNotifier struct {
	cw        metricPutter
	namespace string
}

// NewMetricNotifier builds a MetricNotifier publishing to namespace.
func NewMetricNotifier(cw metricPutter, namespace string) *MetricNotifier {
	return &MetricNotifier{cw: cw, namespace: namespace}
}

// Notify publishes RunSuccess=1 for a finished run and ignores every other event
// (a started run has no outcome yet; a failed run publishes no success).
func (m *MetricNotifier) Notify(ctx context.Context, event CrawlEvent) error {
	fin, ok := event.(RunFinished)
	if !ok {
		return nil
	}
	_, err := m.cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: awssdk.String(m.namespace),
		MetricData: []cwtypes.MetricDatum{{
			MetricName: awssdk.String(MetricRunSuccess),
			Value:      awssdk.Float64(1),
			Unit:       cwtypes.StandardUnitCount,
			Dimensions: []cwtypes.Dimension{{Name: awssdk.String(DimensionSource), Value: awssdk.String(fin.Source)}},
		}},
	})
	if err != nil {
		return fmt.Errorf("crawlnotify: put run metric: %w", err)
	}
	return nil
}

// MultiNotifier fans one event out to several notifiers, best-effort: it calls
// every notifier and never returns an error, so one transport failing (Slack down)
// never suppresses another (the CloudWatch metric) nor aborts the run.
type MultiNotifier []Notifier

// Notify delivers event to every notifier, ignoring individual failures.
func (m MultiNotifier) Notify(ctx context.Context, event CrawlEvent) error {
	for _, n := range m {
		_ = n.Notify(ctx, event)
	}
	return nil
}

// FleetNotifier builds the notifier every ingestion producer runs through: a Slack
// notifier (silent when webhookURL is empty) plus, when runMetricsNamespace is set,
// a CloudWatch run-success metric notifier. A CloudWatch client that cannot be
// built (no AWS credentials, e.g. a local run) logs a warning and degrades to
// Slack-only, so a producer never fails to start over metrics wiring.
func FleetNotifier(ctx context.Context, logger *slog.Logger, webhookURL, runMetricsNamespace string) Notifier {
	base := NewNotifier(webhookURL)
	if runMetricsNamespace == "" {
		return base
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		logger.WarnContext(ctx, "run metrics disabled: could not load AWS config", slog.Any("err", err))
		return base
	}
	return MultiNotifier{base, NewMetricNotifier(cloudwatch.NewFromConfig(cfg), runMetricsNamespace)}
}
