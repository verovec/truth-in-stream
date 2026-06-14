// Package mqmetrics shapes Amazon MQ for RabbitMQ management-API queue stats
// into CloudWatch metric data. It is transport-agnostic: the shaping logic knows
// nothing about AWS or the wire format of PutMetricData, so it is unit-testable,
// and the lambda entrypoint wires it to the real management API and CloudWatch.
package mqmetrics

import "strings"

// APIQueue is the subset of a RabbitMQ management-API queue object the poller
// consumes (GET /api/queues). Fields absent from an idle queue (notably
// message_stats) decode to their zero value.
type APIQueue struct {
	Name         string        `json:"name"`
	Vhost        string        `json:"vhost"`
	Messages     int64         `json:"messages"`
	Consumers    int64         `json:"consumers"`
	MessageStats *MessageStats `json:"message_stats"`
}

// MessageStats holds the rate breakdowns the management API reports for an
// active queue. Only the publish rate is consumed here.
type MessageStats struct {
	PublishDetails RateDetails `json:"publish_details"`
}

// RateDetails is the per-second rate of a message-stats counter.
type RateDetails struct {
	Rate float64 `json:"rate"`
}

// Metric names published to CloudWatch.
const (
	MetricBacklog     = "Backlog"
	MetricConsumers   = "ConsumerCount"
	MetricPublishRate = "PublishRate"
)

// CloudWatch metric units.
const (
	UnitCount       = "Count"
	UnitCountSecond = "Count/Second"
)

// Dimension names. Per-version queues carry Queue (the full versioned name); the
// version-stripped rollup carries QueueBase (the stable base name) so a dashboard
// can reference it without editing when the active version rolls.
const (
	DimensionBroker    = "Broker"
	DimensionQueue     = "Queue"
	DimensionQueueBase = "QueueBase"
)

// Dimension is one CloudWatch metric dimension.
type Dimension struct {
	Name  string
	Value string
}

// Datum is one transport-agnostic metric point. The lambda maps it to a
// CloudWatch MetricDatum.
type Datum struct {
	MetricName string
	Dimensions []Dimension
	Value      float64
	Unit       string
}

// Options configures shaping. Broker is the value of the Broker dimension on
// every datum; Base is the versioned-queue base name (e.g. "embedding.jobs")
// whose .v<version> queues are measured.
type Options struct {
	Broker string
	Base   string
}

// ShapeMetrics turns the queues reported by the management API into CloudWatch
// datums. Only queues named <Base>.v<version> (version a run of letters, digits,
// '_' or '-') are measured; everything else is ignored. Each matched queue emits
// Backlog, ConsumerCount and PublishRate under the Queue dimension, and the same
// three metrics are summed across all matched versions into a version-stripped
// rollup under the QueueBase dimension. No matched queues yields no datums.
func ShapeMetrics(queues []APIQueue, opts Options) []Datum {
	var (
		datums                    []Datum
		sumMessages, sumConsumers int64
		sumRate                   float64
		matched                   bool
	)

	for _, q := range queues {
		if !isVersionedQueue(q.Name, opts.Base) {
			continue
		}
		rate := 0.0
		if q.MessageStats != nil {
			rate = q.MessageStats.PublishDetails.Rate
		}
		dims := []Dimension{
			{Name: DimensionBroker, Value: opts.Broker},
			{Name: DimensionQueue, Value: q.Name},
		}
		datums = append(datums,
			Datum{MetricName: MetricBacklog, Dimensions: dims, Value: float64(q.Messages), Unit: UnitCount},
			Datum{MetricName: MetricConsumers, Dimensions: dims, Value: float64(q.Consumers), Unit: UnitCount},
			Datum{MetricName: MetricPublishRate, Dimensions: dims, Value: rate, Unit: UnitCountSecond},
		)

		sumMessages += q.Messages
		sumConsumers += q.Consumers
		sumRate += rate
		matched = true
	}

	if !matched {
		return datums
	}

	rollup := []Dimension{
		{Name: DimensionBroker, Value: opts.Broker},
		{Name: DimensionQueueBase, Value: opts.Base},
	}
	datums = append(datums,
		Datum{MetricName: MetricBacklog, Dimensions: rollup, Value: float64(sumMessages), Unit: UnitCount},
		Datum{MetricName: MetricConsumers, Dimensions: rollup, Value: float64(sumConsumers), Unit: UnitCount},
		Datum{MetricName: MetricPublishRate, Dimensions: rollup, Value: sumRate, Unit: UnitCountSecond},
	)
	return datums
}

// isVersionedQueue reports whether name is base + ".v" + a version token, where
// the token is a non-empty run of letters, digits, '_' or '-'. This mirrors the
// versioned-queue convention the producer and worker bind to.
func isVersionedQueue(name, base string) bool {
	prefix := base + ".v"
	token, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	return isVersionToken(token)
}

func isVersionToken(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// BatchDatums splits datums into chunks no larger than size. CloudWatch caps
// PutMetricData at 1000 MetricDatum per call. A size of zero or less yields a
// single batch; an empty input yields no batches.
func BatchDatums(datums []Datum, size int) [][]Datum {
	if len(datums) == 0 {
		return nil
	}
	if size <= 0 || len(datums) <= size {
		return [][]Datum{datums}
	}
	batches := make([][]Datum, 0, (len(datums)+size-1)/size)
	for i := 0; i < len(datums); i += size {
		end := i + size
		if end > len(datums) {
			end = len(datums)
		}
		batches = append(batches, datums[i:end])
	}
	return batches
}
