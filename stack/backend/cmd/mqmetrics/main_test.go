package main

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/verovec/truth-in-stream/backend/internal/mqmetrics"
)

type fakeFetcher struct {
	queues []mqmetrics.APIQueue
	err    error
}

func (f fakeFetcher) FetchQueues(context.Context) ([]mqmetrics.APIQueue, error) {
	return f.queues, f.err
}

type fakePutter struct {
	calls []*cloudwatch.PutMetricDataInput
	err   error
}

func (f *fakePutter) PutMetricData(_ context.Context, in *cloudwatch.PutMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return &cloudwatch.PutMetricDataOutput{}, nil
}

func TestPollPublishesShapedMetrics(t *testing.T) {
	t.Parallel()

	fetcher := fakeFetcher{queues: []mqmetrics.APIQueue{
		{Name: "embedding.jobs.v1", Messages: 7, Consumers: 2, MessageStats: &mqmetrics.MessageStats{PublishDetails: mqmetrics.RateDetails{Rate: 3.5}}},
		{Name: "unrelated", Messages: 1},
	}}
	putter := &fakePutter{}

	err := poll(context.Background(), fetcher, putter, mqmetrics.Options{Broker: "b", Bases: []string{"embedding.jobs"}}, "TruthInStream/RabbitMQ")
	if err != nil {
		t.Fatalf("poll() error: %v", err)
	}

	if len(putter.calls) != 1 {
		t.Fatalf("PutMetricData calls = %d, want 1", len(putter.calls))
	}
	got := putter.calls[0]
	if awssdk.ToString(got.Namespace) != "TruthInStream/RabbitMQ" {
		t.Errorf("namespace = %q, want TruthInStream/RabbitMQ", awssdk.ToString(got.Namespace))
	}
	// One matched queue -> 3 per-version datums + 3 rollup datums.
	if len(got.MetricData) != 6 {
		t.Fatalf("MetricData length = %d, want 6", len(got.MetricData))
	}
}

func TestPollFetchErrorSkipsPublish(t *testing.T) {
	t.Parallel()

	putter := &fakePutter{}
	err := poll(context.Background(), fakeFetcher{err: errors.New("broker down")}, putter, mqmetrics.Options{Bases: []string{"embedding.jobs"}}, "ns")
	if err == nil {
		t.Fatal("poll() expected an error when the fetch fails")
	}
	if len(putter.calls) != 0 {
		t.Errorf("PutMetricData called %d times on fetch failure, want 0", len(putter.calls))
	}
}

func TestPublishBatchesAtTheCloudWatchCap(t *testing.T) {
	t.Parallel()

	datums := make([]mqmetrics.Datum, maxDatumsPerCall+1)
	for i := range datums {
		datums[i] = mqmetrics.Datum{MetricName: mqmetrics.MetricBacklog, Unit: mqmetrics.UnitCount}
	}
	putter := &fakePutter{}
	if err := publish(context.Background(), putter, "ns", datums); err != nil {
		t.Fatalf("publish() error: %v", err)
	}
	if len(putter.calls) != 2 {
		t.Fatalf("PutMetricData calls = %d, want 2 (batched at the cap)", len(putter.calls))
	}
	if len(putter.calls[0].MetricData) != maxDatumsPerCall || len(putter.calls[1].MetricData) != 1 {
		t.Errorf("batch sizes = %d,%d; want %d,1", len(putter.calls[0].MetricData), len(putter.calls[1].MetricData), maxDatumsPerCall)
	}
}

func TestPublishEmptyMakesNoCall(t *testing.T) {
	t.Parallel()

	putter := &fakePutter{}
	if err := publish(context.Background(), putter, "ns", nil); err != nil {
		t.Fatalf("publish() error: %v", err)
	}
	if len(putter.calls) != 0 {
		t.Errorf("PutMetricData called %d times for no datums, want 0", len(putter.calls))
	}
}

func TestPublishPropagatesPutError(t *testing.T) {
	t.Parallel()

	putter := &fakePutter{err: errors.New("throttled")}
	datums := []mqmetrics.Datum{{MetricName: mqmetrics.MetricBacklog, Unit: mqmetrics.UnitCount}}
	if err := publish(context.Background(), putter, "ns", datums); err == nil {
		t.Fatal("publish() expected the PutMetricData error to propagate")
	}
}

func TestToMetricData(t *testing.T) {
	t.Parallel()

	datums := []mqmetrics.Datum{{
		MetricName: mqmetrics.MetricPublishRate,
		Dimensions: []mqmetrics.Dimension{
			{Name: mqmetrics.DimensionBroker, Value: "b"},
			{Name: mqmetrics.DimensionQueue, Value: "embedding.jobs.v1"},
		},
		Value: 3.5,
		Unit:  mqmetrics.UnitCountSecond,
	}}

	out := toMetricData(datums)
	if len(out) != 1 {
		t.Fatalf("toMetricData length = %d, want 1", len(out))
	}
	d := out[0]
	if awssdk.ToString(d.MetricName) != mqmetrics.MetricPublishRate {
		t.Errorf("MetricName = %q, want %q", awssdk.ToString(d.MetricName), mqmetrics.MetricPublishRate)
	}
	if d.Unit != cwtypes.StandardUnitCountSecond {
		t.Errorf("Unit = %q, want %q", d.Unit, cwtypes.StandardUnitCountSecond)
	}
	if awssdk.ToFloat64(d.Value) != 3.5 {
		t.Errorf("Value = %v, want 3.5", awssdk.ToFloat64(d.Value))
	}
	if len(d.Dimensions) != 2 {
		t.Fatalf("Dimensions length = %d, want 2", len(d.Dimensions))
	}
	if awssdk.ToString(d.Dimensions[0].Name) != mqmetrics.DimensionBroker || awssdk.ToString(d.Dimensions[0].Value) != "b" {
		t.Errorf("dimension[0] = %q=%q, want Broker=b", awssdk.ToString(d.Dimensions[0].Name), awssdk.ToString(d.Dimensions[0].Value))
	}
	if awssdk.ToString(d.Dimensions[1].Name) != mqmetrics.DimensionQueue {
		t.Errorf("dimension[1] name = %q, want Queue", awssdk.ToString(d.Dimensions[1].Name))
	}
}

type fakeSecrets struct {
	value *string
	err   error
}

func (f fakeSecrets) GetSecretValue(_ context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: f.value}, nil
}

func TestFetchSecret(t *testing.T) {
	t.Parallel()

	val := "amqps://app:pw@host:5671/"
	got, err := fetchSecret(context.Background(), fakeSecrets{value: &val}, "arn")
	if err != nil {
		t.Fatalf("fetchSecret() error: %v", err)
	}
	if got != val {
		t.Errorf("fetchSecret() = %q, want %q", got, val)
	}

	if _, err := fetchSecret(context.Background(), fakeSecrets{value: nil}, "arn"); err == nil {
		t.Error("fetchSecret() expected an error when the secret has no string value")
	}
	if _, err := fetchSecret(context.Background(), fakeSecrets{err: errors.New("denied")}, "arn"); err == nil {
		t.Error("fetchSecret() expected the GetSecretValue error to propagate")
	}
}
