// Package queue is the message-broker adapter for embedding work. It wraps the
// RabbitMQ AMQP 0.9.1 client behind a small publish/consume surface so the
// embedding producer and the worker fleet share one durable, priority-ordered
// queue. It is transport-only: it exposes no domain or HTTP types, so swapping
// brokers touches this package alone.
package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// maxQueuePriority is the AMQP ceiling for a queue's x-max-priority argument: a
// message priority is a single byte, so 255 is the highest the broker honors.
const maxQueuePriority = 255

// versionHeader carries the queue's schema version on every published message,
// so a consumer can reject a message stamped with a version it does not
// understand rather than mis-process it. It travels in the AMQP message headers,
// not the application body, so the producer's payload construction is untouched.
const versionHeader = "x-queue-version"

// Config selects and shapes the embedding-job queue.
//
// URL is the AMQP broker connection string (amqp:// locally, amqps:// against
// Amazon MQ); it carries the credentials and is sourced from configuration
// only, never logged. QueueName is the durable, version-suffixed queue both
// sides bind to (e.g. embedding.jobs.v1); Version is that queue's schema version,
// stamped on every published message. MaxPriority is the queue's x-max-priority
// ceiling (1-255): messages with a higher Priority byte are delivered first, and
// a publish above this value is rejected. Prefetch caps the unacknowledged
// messages the broker pushes to one consumer, giving the worker fleet fair
// dispatch; zero leaves it unbounded.
type Config struct {
	URL         string
	QueueName   string
	Version     string
	MaxPriority uint8
	Prefetch    int
}

// Message is an application payload to enqueue. Priority must not exceed the
// queue's configured MaxPriority; higher numbers are delivered first.
type Message struct {
	Body     []byte
	Priority uint8
}

// Delivery is a consumed message awaiting acknowledgement. The consumer MUST
// call Ack once it has durably handled the message, or Nack to drop or requeue
// it; an unacknowledged delivery is redelivered when its channel closes, so the
// broker never loses work to a crashed worker. Version is the schema version the
// producer stamped, empty if the message carried none.
type Delivery struct {
	Body     []byte
	Priority uint8
	Version  string

	acker amqp.Acknowledger
	tag   uint64
}

// Ack confirms the message was handled, so the broker drops it from the queue.
func (d Delivery) Ack() error {
	if d.acker == nil {
		return errors.New("queue: delivery has no acknowledger")
	}
	if err := d.acker.Ack(d.tag, false); err != nil {
		return fmt.Errorf("queue: ack delivery %d: %w", d.tag, err)
	}
	return nil
}

// Nack rejects the message. With requeue true the broker redelivers it (for a
// transient failure); with requeue false it is discarded or dead-lettered (for
// a poison message the worker must not loop on).
func (d Delivery) Nack(requeue bool) error {
	if d.acker == nil {
		return errors.New("queue: delivery has no acknowledger")
	}
	if err := d.acker.Nack(d.tag, false, requeue); err != nil {
		return fmt.Errorf("queue: nack delivery %d: %w", d.tag, err)
	}
	return nil
}

// Client is a connection to the broker plus a confirm-mode publishing channel.
// One Client is safe for concurrent Publish calls and any number of concurrent
// Consume streams; the publishing channel is serialized internally because an
// AMQP channel is not safe for concurrent use.
type Client struct {
	conn  *amqp.Connection
	pubCh *amqp.Channel

	pubMu sync.Mutex

	// consumers tracks the live Consume goroutines so Close can let them wind
	// down before tearing the connection down, keeping channel operations off
	// the same wire as the connection close.
	consumers sync.WaitGroup

	queueName   string
	version     string
	maxPriority uint8
	prefetch    int
}

// New dials the broker, opens a publishing channel in confirm mode, and
// declares the durable priority queue. It fails fast on invalid configuration
// or any broker error, and on failure leaves no connection open. The queue
// declaration is idempotent, so every process that calls New converges on the
// same durable, priority-enabled queue.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("queue: URL is required")
	}
	if cfg.QueueName == "" {
		return nil, errors.New("queue: queue name is required")
	}
	if cfg.Version == "" {
		return nil, errors.New("queue: version is required")
	}
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("queue: max priority must be in [1, %d], got %d", maxQueuePriority, cfg.MaxPriority)
	}
	if cfg.Prefetch < 0 {
		return nil, fmt.Errorf("queue: prefetch must not be negative, got %d", cfg.Prefetch)
	}

	conn, err := amqp.Dial(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("queue: dial broker: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("queue: open publish channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("queue: enable publisher confirms: %w", err)
	}
	if err := declareQueue(ch, cfg.QueueName, cfg.MaxPriority); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &Client{
		conn:        conn,
		pubCh:       ch,
		queueName:   cfg.QueueName,
		version:     cfg.Version,
		maxPriority: cfg.MaxPriority,
		prefetch:    cfg.Prefetch,
	}, nil
}

// Publish enqueues msg as a persistent message and blocks until the broker
// confirms it, so a successful return means the message is durably queued and a
// broker crash cannot silently drop it. Priority above the queue maximum is a
// caller error, rejected before the message leaves the process. Delivery is
// at-least-once: if ctx is canceled after the frame is sent but before the
// confirm arrives, Publish returns an error though the message may already be
// queued, so a retry can duplicate it; the idempotent worker tolerates that.
func (c *Client) Publish(ctx context.Context, msg Message) error {
	if msg.Priority > c.maxPriority {
		return fmt.Errorf("queue: message priority %d exceeds queue maximum %d", msg.Priority, c.maxPriority)
	}

	// Hold the lock only for the publish frame: an AMQP channel is not safe for
	// concurrent use and deferred confirmations must be sequenced in publish
	// order. Waiting for the confirm outside the lock lets many publishes have
	// confirmations outstanding at once, so one slow confirm does not stall
	// every other producer behind it.
	c.pubMu.Lock()
	confirm, err := c.pubCh.PublishWithDeferredConfirmWithContext(ctx, "", c.queueName, false, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		Priority:     msg.Priority,
		Headers:      amqp.Table{versionHeader: c.version},
		Body:         msg.Body,
	})
	c.pubMu.Unlock()
	if err != nil {
		return fmt.Errorf("queue: publish to %q: %w", c.queueName, err)
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("queue: await publish confirm: %w", err)
	}
	if !acked {
		return fmt.Errorf("queue: broker nacked publish to %q", c.queueName)
	}
	return nil
}

// Consume opens a dedicated channel and returns a stream of deliveries the
// caller acknowledges one by one. The stream and its channel close when ctx is
// canceled; any delivery the broker had handed out but the consumer had not yet
// acknowledged is requeued when the channel closes, so canceling never drops a
// message. Each Consume call is an independent consumer; running several scales a
// worker fleet across one queue. Cancel the context to stop a consumer, then call
// Close.
func (c *Client) Consume(ctx context.Context) (<-chan Delivery, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("queue: open consume channel: %w", err)
	}
	if c.prefetch > 0 {
		if err := ch.Qos(c.prefetch, 0, false); err != nil {
			_ = ch.Close()
			return nil, fmt.Errorf("queue: set prefetch: %w", err)
		}
	}
	// The queue already exists broker-wide: New declares it for every Client
	// (including a consumer-only worker), so Consume does not redeclare it.
	//
	// Plain Consume rather than ConsumeWithContext: cancellation is handled in
	// the single forward goroutine below, so every operation on this channel has
	// exactly one owner. Letting the library cancel the consumer concurrently
	// would race the channel close on the wire.
	raw, err := ch.Consume(c.queueName, "", false, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("queue: consume %q: %w", c.queueName, err)
	}

	out := make(chan Delivery)
	c.consumers.Add(1)
	go func() {
		defer c.consumers.Done()
		forward(ctx, ch, raw, out)
	}()
	return out, nil
}

// forward translates broker deliveries into the transport-free Delivery type
// until ctx is canceled or the broker closes the stream, then closes the output
// channel and its consume channel. Closing the channel requeues any delivery the
// consumer had not yet acknowledged, so an unhandled in-flight message is never
// lost (it is redelivered at-least-once). It is the sole owner of ch, so its
// channel operations never race.
func forward(ctx context.Context, ch *amqp.Channel, raw <-chan amqp.Delivery, out chan<- Delivery) {
	defer close(out)
	defer func() { _ = ch.Close() }()

	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-raw:
			if !ok {
				return
			}
			version, _ := d.Headers[versionHeader].(string)
			wrapped := Delivery{Body: d.Body, Priority: d.Priority, Version: version, acker: d.Acknowledger, tag: d.DeliveryTag}
			select {
			case out <- wrapped:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Close tears down the client. It closes the connection first, which closes the
// publishing channel and every consume channel and so ends every forward
// goroutine; it then waits for those goroutines to finish. Closing the
// connection before waiting means Close cannot deadlock even if a caller forgot
// to cancel a Consume context. Any delivery a consumer had not yet acknowledged
// is requeued by the broker on channel close, so nothing is lost.
func (c *Client) Close() error {
	var errs []error
	if err := c.conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		errs = append(errs, fmt.Errorf("queue: close connection: %w", err))
	}
	c.consumers.Wait()
	return errors.Join(errs...)
}

// declareQueue declares the durable, priority-enabled embedding-job queue. It is
// idempotent for matching arguments, so producer and consumers can each declare
// it; x-max-priority must be set at declaration and cannot be changed later
// without deleting the queue.
func declareQueue(ch *amqp.Channel, name string, maxPriority uint8) error {
	if _, err := ch.QueueDeclare(name, true, false, false, false, amqp.Table{
		"x-max-priority": int(maxPriority),
	}); err != nil {
		return fmt.Errorf("queue: declare %q: %w", name, err)
	}
	return nil
}
