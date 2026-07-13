package crawlnotify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
)

// fakePutter records PutMetricData calls so a test can assert the emitted metric.
type fakePutter struct {
	inputs []*cloudwatch.PutMetricDataInput
	err    error
}

func (f *fakePutter) PutMetricData(_ context.Context, in *cloudwatch.PutMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error) {
	f.inputs = append(f.inputs, in)
	return &cloudwatch.PutMetricDataOutput{}, f.err
}

func TestMetricNotifierPublishesRunSuccessOnFinish(t *testing.T) {
	t.Parallel()
	cw := &fakePutter{}
	m := NewMetricNotifier(cw, "TruthInStream/Ingestion")

	if err := m.Notify(t.Context(), RunFinished{Source: "wikipedia", Scope: "Category:Physics", New: 3, Duration: time.Second}); err != nil {
		t.Fatalf("Notify(RunFinished) error = %v", err)
	}
	if len(cw.inputs) != 1 {
		t.Fatalf("PutMetricData called %d times, want 1", len(cw.inputs))
	}
	in := cw.inputs[0]
	if *in.Namespace != "TruthInStream/Ingestion" {
		t.Fatalf("namespace = %q, want TruthInStream/Ingestion", *in.Namespace)
	}
	if len(in.MetricData) != 1 {
		t.Fatalf("metric data len = %d, want 1", len(in.MetricData))
	}
	d := in.MetricData[0]
	if *d.MetricName != MetricRunSuccess || *d.Value != 1 {
		t.Fatalf("metric = %s=%v, want %s=1", *d.MetricName, *d.Value, MetricRunSuccess)
	}
	if len(d.Dimensions) != 1 || *d.Dimensions[0].Name != DimensionSource || *d.Dimensions[0].Value != "wikipedia" {
		t.Fatalf("dimensions = %+v, want Source=wikipedia", d.Dimensions)
	}
}

func TestMetricNotifierIgnoresNonFinishEvents(t *testing.T) {
	t.Parallel()
	cw := &fakePutter{}
	m := NewMetricNotifier(cw, "ns")

	// A started run has no outcome; a failed run publishes no success.
	if err := m.Notify(t.Context(), RunStarted{Source: "s", Scope: "x"}); err != nil {
		t.Fatalf("Notify(RunStarted) error = %v", err)
	}
	if err := m.Notify(t.Context(), RunFailed{Source: "s", Scope: "x", Err: errors.New("boom")}); err != nil {
		t.Fatalf("Notify(RunFailed) error = %v", err)
	}
	if len(cw.inputs) != 0 {
		t.Fatalf("PutMetricData called %d times, want 0 (only a finished run emits)", len(cw.inputs))
	}
}

// recordNotifier records the events it received, for the MultiNotifier fan-out test.
type recordNotifier struct {
	events []CrawlEvent
	err    error
}

func (r *recordNotifier) Notify(_ context.Context, e CrawlEvent) error {
	r.events = append(r.events, e)
	return r.err
}

func TestMultiNotifierFansOutAndSwallowsErrors(t *testing.T) {
	t.Parallel()
	a := &recordNotifier{err: errors.New("slack down")} // one transport failing
	b := &recordNotifier{}
	multi := MultiNotifier{a, b}

	ev := RunFinished{Source: "s", Scope: "x"}
	if err := multi.Notify(t.Context(), ev); err != nil {
		t.Fatalf("MultiNotifier.Notify error = %v, want nil (best-effort fan-out)", err)
	}
	if len(a.events) != 1 || len(b.events) != 1 {
		t.Fatalf("fan-out delivered a=%d b=%d, want both 1 (one failing must not suppress the other)", len(a.events), len(b.events))
	}
}
