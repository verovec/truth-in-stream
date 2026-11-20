package mqmetrics

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestShapeMetrics(t *testing.T) {
	t.Parallel()

	stats := func(rate float64) *MessageStats {
		return &MessageStats{PublishDetails: RateDetails{Rate: rate}}
	}

	tests := []struct {
		name   string
		queues []APIQueue
		opts   Options
		want   []Datum
	}{
		{
			name: "single versioned queue emits per-version and rollup datums",
			queues: []APIQueue{
				{Name: "embedding.jobs.v1", Messages: 7, Consumers: 2, MessageStats: stats(3.5)},
			},
			opts: Options{Broker: "truth-in-stream-dev", Bases: []string{"embedding.jobs"}},
			want: []Datum{
				{MetricName: MetricBacklog, Dimensions: queueDims("truth-in-stream-dev", "embedding.jobs.v1"), Value: 7, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: queueDims("truth-in-stream-dev", "embedding.jobs.v1"), Value: 2, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: queueDims("truth-in-stream-dev", "embedding.jobs.v1"), Value: 3.5, Unit: UnitCountSecond},
				{MetricName: MetricBacklog, Dimensions: rollupDims("truth-in-stream-dev"), Value: 7, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: rollupDims("truth-in-stream-dev"), Value: 2, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: rollupDims("truth-in-stream-dev"), Value: 3.5, Unit: UnitCountSecond},
			},
		},
		{
			name: "multiple versions roll up by summing across versions",
			queues: []APIQueue{
				{Name: "embedding.jobs.v1", Messages: 4, Consumers: 1, MessageStats: stats(1.0)},
				{Name: "embedding.jobs.v2", Messages: 6, Consumers: 3, MessageStats: stats(2.0)},
			},
			opts: Options{Broker: "b", Bases: []string{"embedding.jobs"}},
			want: []Datum{
				{MetricName: MetricBacklog, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 4, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 1, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 1.0, Unit: UnitCountSecond},
				{MetricName: MetricBacklog, Dimensions: queueDims("b", "embedding.jobs.v2"), Value: 6, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: queueDims("b", "embedding.jobs.v2"), Value: 3, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: queueDims("b", "embedding.jobs.v2"), Value: 2.0, Unit: UnitCountSecond},
				{MetricName: MetricBacklog, Dimensions: rollupDims("b"), Value: 10, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: rollupDims("b"), Value: 4, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: rollupDims("b"), Value: 3.0, Unit: UnitCountSecond},
			},
		},
		{
			name: "queues not matching the base version pattern are ignored",
			queues: []APIQueue{
				{Name: "embedding.jobs.v1", Messages: 1, Consumers: 0, MessageStats: stats(0)},
				{Name: "embedding.jobs", Messages: 99, Consumers: 9},         // no version suffix
				{Name: "other.queue.v1", Messages: 5, Consumers: 5},          // different base
				{Name: "embedding.jobs.v1.extra", Messages: 5, Consumers: 5}, // trailing segment, not a version token
				{Name: "embedding.jobs.vbad.token", Messages: 5},             // dot in version token
			},
			opts: Options{Broker: "b", Bases: []string{"embedding.jobs"}},
			want: []Datum{
				{MetricName: MetricBacklog, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 1, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 0, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 0, Unit: UnitCountSecond},
				{MetricName: MetricBacklog, Dimensions: rollupDims("b"), Value: 1, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: rollupDims("b"), Value: 0, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: rollupDims("b"), Value: 0, Unit: UnitCountSecond},
			},
		},
		{
			name: "missing message_stats yields a zero publish rate",
			queues: []APIQueue{
				{Name: "embedding.jobs.v1", Messages: 2, Consumers: 1, MessageStats: nil},
			},
			opts: Options{Broker: "b", Bases: []string{"embedding.jobs"}},
			want: []Datum{
				{MetricName: MetricBacklog, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 2, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 1, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: queueDims("b", "embedding.jobs.v1"), Value: 0, Unit: UnitCountSecond},
				{MetricName: MetricBacklog, Dimensions: rollupDims("b"), Value: 2, Unit: UnitCount},
				{MetricName: MetricConsumers, Dimensions: rollupDims("b"), Value: 1, Unit: UnitCount},
				{MetricName: MetricPublishRate, Dimensions: rollupDims("b"), Value: 0, Unit: UnitCountSecond},
			},
		},
		{
			name:   "no matching queues yields no datums and no rollup",
			queues: []APIQueue{{Name: "unrelated", Messages: 3}},
			opts:   Options{Broker: "b", Bases: []string{"embedding.jobs"}},
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ShapeMetrics(tt.queues, tt.opts)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ShapeMetrics() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestShapeMetricsMeasuresDLQAndMultipleBases proves every configured base's
// versioned queues AND its dead-letter queues are measured, each under its own
// QueueBase rollup, so a DLQ-depth alarm can key on a *.dlq QueueBase.
func TestShapeMetricsMeasuresDLQAndMultipleBases(t *testing.T) {
	t.Parallel()
	queues := []APIQueue{
		{Name: "embedding.jobs.v2", Messages: 5, Consumers: 3},
		{Name: "embedding.jobs.dlq.v2", Messages: 2, Consumers: 0},
		{Name: "crawl.chunks.v2", Messages: 1, Consumers: 1},
		{Name: "unrelated.queue", Messages: 99, Consumers: 0},
	}
	got := ShapeMetrics(queues, Options{Broker: "b", Bases: []string{"embedding.jobs", "crawl.chunks"}})

	// A helper to find a Backlog datum by its QueueBase rollup value.
	rollupBacklog := func(base string) (float64, bool) {
		for _, d := range got {
			if d.MetricName != MetricBacklog {
				continue
			}
			for _, dim := range d.Dimensions {
				if dim.Name == DimensionQueueBase && dim.Value == base {
					return d.Value, true
				}
			}
		}
		return 0, false
	}

	if v, ok := rollupBacklog("embedding.jobs.dlq"); !ok || v != 2 {
		t.Fatalf("DLQ rollup backlog = %v (found=%v), want 2 under QueueBase embedding.jobs.dlq", v, ok)
	}
	if v, ok := rollupBacklog("embedding.jobs"); !ok || v != 5 {
		t.Fatalf("base rollup backlog = %v (found=%v), want 5", v, ok)
	}
	if v, ok := rollupBacklog("crawl.chunks"); !ok || v != 1 {
		t.Fatalf("second base rollup backlog = %v (found=%v), want 1", v, ok)
	}
	// The unrelated queue is never measured.
	for _, d := range got {
		for _, dim := range d.Dimensions {
			if dim.Value == "unrelated.queue" {
				t.Fatal("an unrelated queue was measured")
			}
		}
	}
}

func TestBatchDatums(t *testing.T) {
	t.Parallel()

	mk := func(n int) []Datum {
		d := make([]Datum, n)
		for i := range d {
			d[i] = Datum{MetricName: MetricBacklog}
		}
		return d
	}

	tests := []struct {
		name      string
		count     int
		size      int
		wantSizes []int
	}{
		{name: "empty yields no batches", count: 0, size: 10, wantSizes: nil},
		{name: "fits in one batch", count: 5, size: 10, wantSizes: []int{5}},
		{name: "exact multiple", count: 20, size: 10, wantSizes: []int{10, 10}},
		{name: "remainder batch", count: 23, size: 10, wantSizes: []int{10, 10, 3}},
		{name: "non-positive size yields a single batch", count: 4, size: 0, wantSizes: []int{4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			batches := BatchDatums(mk(tt.count), tt.size)
			var sizes []int
			total := 0
			for _, b := range batches {
				sizes = append(sizes, len(b))
				total += len(b)
			}
			if diff := cmp.Diff(tt.wantSizes, sizes); diff != "" {
				t.Errorf("batch sizes mismatch (-want +got):\n%s", diff)
			}
			if total != tt.count {
				t.Errorf("batched %d datums, want %d", total, tt.count)
			}
		})
	}
}

func queueDims(broker, queue string) []Dimension {
	return []Dimension{{Name: DimensionBroker, Value: broker}, {Name: DimensionQueue, Value: queue}}
}

func rollupDims(broker string) []Dimension {
	return []Dimension{{Name: DimensionBroker, Value: broker}, {Name: DimensionQueueBase, Value: "embedding.jobs"}}
}
