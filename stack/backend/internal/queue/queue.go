// Package queue is the message-broker adapter for the ingestion pipelines. It
// wraps the RabbitMQ AMQP 0.9.1 client behind a small publish/consume surface so
// every producer and the worker fleet share one durable, priority-ordered queue.
// It is transport-only: it exposes no domain or HTTP types, so swapping brokers
// touches this package alone.
//
// The client is self-healing. A broker restart, a network blip, or the weekly
// Amazon MQ maintenance reboot closes the underlying connection; the client
// redials with exponential backoff and jitter, re-declares its topology, and
// re-establishes publishers and consumers transparently, so a caller's Publish
// blocks across the outage and a Consume stream survives it without a call-site
// change. Delivery stays at-least-once: workers are idempotent upserts. Nothing
// is dropped silently: a message a consumer rejects (a poison message or one past
// its retry budget) is dead-lettered to a companion per-queue DLQ the operator can
// inspect and replay, not acked away.
package queue

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

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

// Reconnect backoff defaults. The first redial waits up to defaultMinBackoff,
// each subsequent one doubles up to defaultMaxBackoff, and every wait is jittered
// down to half its ceiling so a fleet of workers reconnecting after the same
// broker restart does not thunder in lockstep.
const (
	defaultMinBackoff = 250 * time.Millisecond
	defaultMaxBackoff = 30 * time.Second
)

// ErrClosed is returned by Publish (and ends a Consume stream) once the client
// has been Closed: the transport is gone for good, so the caller must stop rather
// than wait for a reconnect that will never come.
var ErrClosed = errors.New("queue: client closed")

// Config selects and shapes an ingestion queue.
//
// URL is the AMQP broker connection string (amqp:// locally, amqps:// against
// Amazon MQ); it carries the credentials and is sourced from configuration only,
// never logged. QueueName is the durable, version-suffixed queue both sides bind
// to (e.g. embedding.jobs.v1); Version is that queue's schema version, stamped on
// every published message. MaxPriority is the queue's x-max-priority ceiling
// (1-255): messages with a higher Priority byte are delivered first, and a publish
// above this value is rejected. Prefetch caps the unacknowledged messages the
// broker pushes to one consumer, giving the worker fleet fair dispatch; zero
// leaves it unbounded.
//
// DisableDLQ turns off dead-letter routing: with it false (the default) each
// queue is declared with a dead-letter exchange pointing at a companion
// <base>.dlq.v<n> queue, so a rejected message is parked, not discarded. It must
// be set identically on every process that declares the queue, or the declarations
// conflict; configuration forwards one value to producers and consumers alike.
// MinBackoff and MaxBackoff bound the reconnect wait; zero selects the package
// defaults.
type Config struct {
	URL         string
	QueueName   string
	Version     string
	MaxPriority uint8
	Prefetch    int

	DisableDLQ bool
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// Message is an application payload to enqueue. Priority must not exceed the
// queue's configured MaxPriority; higher numbers are delivered first.
type Message struct {
	Body     []byte
	Priority uint8
}

// Delivery is a consumed message awaiting acknowledgement. The consumer MUST call
// Ack once it has durably handled the message, Nack(true) to requeue it (a
// transient failure or shutdown), or Nack(false) to reject it, which dead-letters
// it to the DLQ rather than discarding it. An unacknowledged delivery is
// redelivered when its channel closes, so the broker never loses work to a crashed
// worker or a dropped connection. Version is the schema version the producer
// stamped, empty if the message carried none.
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
// transient failure or a shutdown); with requeue false it is dead-lettered to the
// queue's DLQ (for a poison message or one past its retry budget the worker must
// not loop on), or discarded if the queue was declared without a DLQ.
func (d Delivery) Nack(requeue bool) error {
	if d.acker == nil {
		return errors.New("queue: delivery has no acknowledger")
	}
	if err := d.acker.Nack(d.tag, false, requeue); err != nil {
		return fmt.Errorf("queue: nack delivery %d: %w", d.tag, err)
	}
	return nil
}

// Client is a self-healing connection to the broker plus a confirm-mode
// publishing channel. One Client is safe for concurrent Publish calls and any
// number of concurrent Consume streams; the publishing channel is serialized
// internally because an AMQP channel is not safe for concurrent use. A background
// supervisor redials on connection loss and swaps in a fresh connection and
// channel, so callers never see the churn.
type Client struct {
	cfg        Config
	dlqName    string
	minBackoff time.Duration
	maxBackoff time.Duration

	// mu guards the mutable connection state (conn, pubCh, stateCh, closed).
	// stateCh is closed to broadcast a state change (a connect, a drop, or Close);
	// waiters re-read the state after it fires, which is how Publish and Consume
	// block until a reconnect completes without busy-waiting.
	mu      sync.Mutex
	conn    *amqp.Connection
	pubCh   *amqp.Channel
	stateCh chan struct{}
	closed  bool

	// pubMu serializes publish frames on pubCh: an AMQP channel is not safe for
	// concurrent use and deferred confirmations must be sequenced in publish order.
	pubMu sync.Mutex

	// stopCh is closed by Close to unblock the supervisor, redial loops, and every
	// consumer; wg tracks those goroutines so Close can drain them.
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New dials the broker, opens a publishing channel in confirm mode, declares the
// durable priority queue (and its dead-letter companion), and starts the reconnect
// supervisor. It fails fast on invalid configuration or the first dial error, and
// on failure leaves no connection open. The queue declaration is idempotent, so
// every process that calls New converges on the same durable, priority-enabled,
// dead-lettered queue.
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

	c := &Client{
		cfg:        cfg,
		dlqName:    deriveDLQName(cfg.QueueName, cfg.Version),
		minBackoff: orDefault(cfg.MinBackoff, defaultMinBackoff),
		maxBackoff: orDefault(cfg.MaxBackoff, defaultMaxBackoff),
		stateCh:    make(chan struct{}),
		stopCh:     make(chan struct{}),
	}
	if c.maxBackoff < c.minBackoff {
		c.maxBackoff = c.minBackoff
	}

	conn, pubCh, err := c.connect()
	if err != nil {
		return nil, err
	}
	c.conn, c.pubCh = conn, pubCh

	c.wg.Add(1)
	go c.supervise()
	return c, nil
}

// orDefault returns d when v is not positive, so a zero Config field selects the
// package default.
func orDefault(v, d time.Duration) time.Duration {
	if v <= 0 {
		return d
	}
	return v
}

// deriveDLQName builds the companion dead-letter queue name for a versioned
// queue: embedding.jobs.v1 (version "1") yields embedding.jobs.dlq.v1, so the DLQ
// carries the same version suffix as the queue it captures failures from. A queue
// name that does not end in the expected suffix falls back to appending .dlq,
// keeping the name unique.
func deriveDLQName(queueName, version string) string {
	suffix := ".v" + version
	base := strings.TrimSuffix(queueName, suffix)
	if base == queueName {
		return queueName + ".dlq"
	}
	return base + ".dlq" + suffix
}

// reopen dials the broker and opens a confirm-mode publishing channel, without
// declaring topology. On any failure it leaves no connection open.
func (c *Client) reopen() (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.Dial(c.cfg.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("queue: dial broker: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("queue: open publish channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("queue: enable publisher confirms: %w", err)
	}
	return conn, ch, nil
}

// connect dials the broker, opens a confirm-mode publishing channel, and declares
// the queue topology. It is used for the initial connection only; a reconnect uses
// reopen and does not re-declare, because the durable queue survives a broker
// restart and re-declaring on every redial would turn a persistent topology
// mismatch (a queue that predates DLQ routing, or a manually diverged queue) into
// an infinite backoff loop rather than letting the existing queue serve. On any
// failure it leaves no connection open.
func (c *Client) connect() (*amqp.Connection, *amqp.Channel, error) {
	conn, ch, err := c.reopen()
	if err != nil {
		return nil, nil, err
	}
	if err := declareTopology(ch, c.cfg, c.dlqName); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, ch, nil
}

// supervise watches the live connection and its publish channel, healing whichever
// closes. A dropped connection triggers a full redial; a publish channel that
// closes on its own (a channel-level exception while the connection stays healthy)
// is replaced without tearing down the connection or its consumers. It runs until
// Close signals stopCh (or a heal is abandoned because the client is closing),
// broadcasting each change so blocked Publish calls and Consume streams
// re-establish themselves.
func (c *Client) supervise() {
	defer c.wg.Done()
	for {
		c.mu.Lock()
		conn, pubCh := c.conn, c.pubCh
		c.mu.Unlock()
		if conn == nil {
			return
		}

		connClosed := make(chan *amqp.Error, 1)
		conn.NotifyClose(connClosed)
		chanClosed := make(chan *amqp.Error, 1)
		pubCh.NotifyClose(chanClosed)

		select {
		case <-c.stopCh:
			return
		case <-connClosed:
			if !c.reconnect() {
				return // client closed mid-redial
			}
		case <-chanClosed:
			// Only the publish channel closed. If the connection is dropping too,
			// reconnect fully; otherwise the connection is healthy, so replace just
			// the channel rather than churning the connection and every consumer.
			if conn.IsClosed() {
				if !c.reconnect() {
					return
				}
			} else if !c.replacePubChannel(conn) {
				return
			}
		}
	}
}

// reconnect marks the client disconnected, waking every waiter, then redials with
// backoff and swaps in the fresh connection and publish channel. It returns false
// if the client was closed during the outage, so the supervisor stops.
func (c *Client) reconnect() bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.conn, c.pubCh = nil, nil
	c.broadcastLocked()
	c.mu.Unlock()

	newConn, newPubCh := c.redial()
	if newConn == nil {
		return false
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = newConn.Close()
		return false
	}
	c.conn, c.pubCh = newConn, newPubCh
	c.broadcastLocked()
	c.mu.Unlock()
	return true
}

// replacePubChannel opens a fresh confirm-mode publish channel on the still-live
// connection and swaps it in, so a publish blocked on the closed channel resumes
// without a full reconnect. If opening the channel fails the connection is dropping
// after all, so it forces a full reconnect. It returns false when the client is
// closed, so the supervisor stops.
func (c *Client) replacePubChannel(conn *amqp.Connection) bool {
	ch, err := conn.Channel()
	if err == nil {
		if cerr := ch.Confirm(false); cerr != nil {
			_ = ch.Close()
			err = cerr
		}
	}
	if err != nil {
		_ = conn.Close()
		return c.reconnect()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = ch.Close()
		return false
	}
	c.pubCh = ch
	c.broadcastLocked()
	c.mu.Unlock()
	return true
}

// redial reconnects with exponential backoff and jitter, retrying until it
// succeeds or Close signals stopCh, in which case it returns a nil connection. It
// reopens the connection and publish channel without re-declaring topology (see
// connect), so a redial after a broker restart is fast and cannot livelock on a
// persistent topology mismatch.
func (c *Client) redial() (*amqp.Connection, *amqp.Channel) {
	for attempt := 0; ; attempt++ {
		select {
		case <-c.stopCh:
			return nil, nil
		default:
		}
		conn, pubCh, err := c.reopen()
		if err == nil {
			return conn, pubCh
		}
		select {
		case <-c.stopCh:
			return nil, nil
		case <-time.After(c.backoff(attempt)):
		}
	}
}

// backoff returns the wait before redial attempt (0-based), a jittered value in
// the window backoffWindow reports for that attempt.
func (c *Client) backoff(attempt int) time.Duration {
	lo, hi := backoffWindow(attempt, c.minBackoff, c.maxBackoff)
	if hi <= lo {
		return lo
	}
	return lo + rand.N(hi-lo)
}

// backoffWindow reports the [lo, hi] delay range for a redial attempt (0-based):
// the ceiling is min doubled per attempt, capped at max; the floor is half the
// ceiling, so the jittered wait spans [ceiling/2, ceiling]. It is a pure function
// so the growth and cap are pinned by a unit test.
func backoffWindow(attempt int, minBackoff, maxBackoff time.Duration) (lo, hi time.Duration) {
	if minBackoff <= 0 {
		minBackoff = defaultMinBackoff
	}
	if maxBackoff < minBackoff {
		maxBackoff = minBackoff
	}
	ceiling := minBackoff
	for range attempt {
		ceiling *= 2
		if ceiling <= 0 || ceiling >= maxBackoff {
			ceiling = maxBackoff
			break
		}
	}
	return ceiling / 2, ceiling
}

// broadcastLocked wakes every goroutine blocked on the current stateCh and arms a
// fresh one for the next wait. The caller must hold mu.
func (c *Client) broadcastLocked() {
	close(c.stateCh)
	c.stateCh = make(chan struct{})
}

// Publish enqueues msg as a persistent message and blocks until the broker
// confirms it, so a successful return means the message is durably queued and a
// broker crash cannot silently drop it. If the connection is down it waits for the
// supervisor to reconnect (bounded only by ctx), then publishes on the fresh
// channel, so a producer running across a broker restart completes rather than
// failing. Priority above the queue maximum is a caller error, rejected before the
// message leaves the process. Delivery is at-least-once: a retry across a
// reconnect can duplicate a message the broker had already queued, which the
// idempotent worker tolerates.
func (c *Client) Publish(ctx context.Context, msg Message) error {
	if msg.Priority > c.cfg.MaxPriority {
		return fmt.Errorf("queue: message priority %d exceeds queue maximum %d", msg.Priority, c.cfg.MaxPriority)
	}

	for {
		ch, err := c.currentPubCh(ctx)
		if err != nil {
			return fmt.Errorf("queue: publish to %q: %w", c.cfg.QueueName, err)
		}

		c.pubMu.Lock()
		confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, "", c.cfg.QueueName, false, false, persistentPublishing(c.cfg.Version, msg))
		c.pubMu.Unlock()
		if err != nil {
			if retry, rerr := c.retryPublish(ctx, err); !retry {
				return fmt.Errorf("queue: publish to %q: %w", c.cfg.QueueName, rerr)
			}
			continue
		}

		acked, err := confirm.WaitContext(ctx)
		if err != nil {
			if retry, rerr := c.retryPublish(ctx, err); !retry {
				return fmt.Errorf("queue: await publish confirm: %w", rerr)
			}
			continue
		}
		if !acked {
			// A connection drop during the confirm wait resolves the deferred
			// confirmation as not-acked with a nil error, and the message was never
			// queued. Distinguish that from a genuine broker nack by the channel
			// state: a closed channel means a drop, so retry across the reconnect
			// rather than failing the producer.
			if ch.IsClosed() {
				if retry, rerr := c.retryPublish(ctx, amqp.ErrClosed); !retry {
					return fmt.Errorf("queue: publish to %q: %w", c.cfg.QueueName, rerr)
				}
				continue
			}
			return fmt.Errorf("queue: broker nacked publish to %q", c.cfg.QueueName)
		}
		return nil
	}
}

// retryPublish classifies a failed publish. A ctx error is terminal (the caller
// gave up), and a non-connection error is a genuine publish failure returned as-is.
// A closed-connection error is retryable: it does not wait here, so the Publish
// loop calls currentPubCh, which re-checks the live channel and blocks only if one
// is not yet available. Waiting here would miss a reconnect that completed between
// the failure and the wait, stalling the publish until its deadline; the terminal
// closed-client case is handled by currentPubCh returning ErrClosed.
func (c *Client) retryPublish(ctx context.Context, cause error) (retry bool, err error) {
	if ctx.Err() != nil {
		return false, cause
	}
	if !errors.Is(cause, amqp.ErrClosed) {
		return false, cause
	}
	return true, nil
}

// currentPubCh returns the live publishing channel, blocking until the supervisor
// has one after a reconnect. A channel that has closed under a drop but not yet
// been swapped out is treated as unavailable, so a retry never publishes on a dead
// channel. It returns ErrClosed once the client is closed and ctx.Err() if the
// caller's context ends first.
func (c *Client) currentPubCh(ctx context.Context) (*amqp.Channel, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, ErrClosed
		}
		ch := c.pubCh
		wait := c.stateCh
		c.mu.Unlock()
		if ch != nil && !ch.IsClosed() {
			return ch, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.stopCh:
			return nil, ErrClosed
		case <-wait:
		}
	}
}

// persistentPublishing builds the AMQP envelope for msg: a persistent delivery
// mode so an enqueued message survives a broker restart, the queue's schema
// version stamped in the headers, and the caller's priority and body. Persistence
// paired with a durable queue (see declareTopology) is the broker-restart half of
// the at-least-once guarantee; it is a pure builder so the persistence guarantee
// can be pinned by a unit test without a live broker.
func persistentPublishing(version string, msg Message) amqp.Publishing {
	return amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		Priority:     msg.Priority,
		Headers:      amqp.Table{versionHeader: version},
		Body:         msg.Body,
	}
}

// Consume returns a stream of deliveries the caller acknowledges one by one. The
// stream survives broker restarts: when the connection drops the supervisor
// redials and the consumer transparently re-subscribes, so the caller's range
// loop never ends on a blip. The stream and its channel close only when ctx is
// canceled or the client is Closed; any delivery the broker had handed out but the
// consumer had not yet acknowledged is requeued when its channel closes, so a
// reconnect never drops a message. Each Consume call is an independent consumer;
// running several scales a worker fleet across one queue.
func (c *Client) Consume(ctx context.Context) (<-chan Delivery, error) {
	out := make(chan Delivery)
	c.wg.Add(1)
	go c.runConsumer(ctx, out)
	return out, nil
}

// runConsumer drives one Consume stream across reconnects. It subscribes on the
// live connection, forwards deliveries until that channel closes (a drop or a
// cancel), and re-subscribes on the next healthy connection, closing out only when
// ctx is canceled or the client is closed.
func (c *Client) runConsumer(ctx context.Context, out chan Delivery) {
	defer c.wg.Done()
	defer close(out)

	failures := 0
	for {
		conn := c.waitConn(ctx)
		if conn == nil {
			return // ctx canceled or client closed
		}
		ch, raw, err := openConsume(conn, c.cfg.QueueName, c.cfg.Prefetch)
		if err != nil {
			// Subscribing failed on a connection that may still be healthy (a
			// channel-level error), which no state change would ever wake. Back off
			// and retry so a transient error self-heals; if the connection is in fact
			// dropping, the next waitConn blocks until the supervisor reconnects.
			failures++
			if !c.sleepBackoff(ctx, failures-1) {
				return
			}
			continue
		}
		failures = 0
		stopped := forwardDeliveries(ctx, raw, out)
		_ = ch.Close()
		if stopped {
			return // ctx canceled: the caller wants to stop
		}
		// raw closed because the connection dropped; loop to re-subscribe once the
		// supervisor has reconnected.
	}
}

// sleepBackoff waits the reconnect backoff for the given attempt, returning false
// if the client is closed or ctx ends first so the caller stops instead of looping.
func (c *Client) sleepBackoff(ctx context.Context, attempt int) bool {
	select {
	case <-ctx.Done():
		return false
	case <-c.stopCh:
		return false
	case <-time.After(c.backoff(attempt)):
		return true
	}
}

// waitConn returns the live connection, blocking until the supervisor has one
// after a reconnect. It returns nil once the client is closed or ctx ends.
func (c *Client) waitConn(ctx context.Context) *amqp.Connection {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil
		}
		conn := c.conn
		wait := c.stateCh
		c.mu.Unlock()
		if conn != nil {
			return conn
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.stopCh:
			return nil
		case <-wait:
		}
	}
}

// openConsume opens a dedicated channel on conn, applies the prefetch QoS, and
// starts consuming the queue with manual acks. The queue already exists broker-wide
// (New declares it for every Client), so this does not redeclare it.
func openConsume(conn *amqp.Connection, queueName string, prefetch int) (*amqp.Channel, <-chan amqp.Delivery, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, nil, fmt.Errorf("queue: open consume channel: %w", err)
	}
	if prefetch > 0 {
		if err := ch.Qos(prefetch, 0, false); err != nil {
			_ = ch.Close()
			return nil, nil, fmt.Errorf("queue: set prefetch: %w", err)
		}
	}
	raw, err := ch.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		return nil, nil, fmt.Errorf("queue: consume %q: %w", queueName, err)
	}
	return ch, raw, nil
}

// forwardDeliveries translates broker deliveries into the transport-free Delivery
// type and forwards them to out until ctx is canceled or the raw stream closes. It
// reports true when ctx canceled the loop (the caller should stop) and false when
// the raw stream closed on its own (a dropped connection, so the caller should
// re-subscribe).
func forwardDeliveries(ctx context.Context, raw <-chan amqp.Delivery, out chan<- Delivery) (stopped bool) {
	for {
		select {
		case <-ctx.Done():
			return true
		case d, ok := <-raw:
			if !ok {
				return false
			}
			version, _ := d.Headers[versionHeader].(string)
			wrapped := Delivery{Body: d.Body, Priority: d.Priority, Version: version, acker: d.Acknowledger, tag: d.DeliveryTag}
			select {
			case out <- wrapped:
			case <-ctx.Done():
				return true
			}
		}
	}
}

// Close tears down the client. It marks the client closed, unblocks the supervisor
// and every waiter, closes the connection (which ends every consumer's channel),
// and waits for the supervisor and consumer goroutines to finish. Closing before
// waiting means Close cannot deadlock even if a caller forgot to cancel a Consume
// context. Any delivery a consumer had not yet acknowledged is requeued by the
// broker on channel close, so nothing is lost.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.conn, c.pubCh = nil, nil
	close(c.stopCh)
	c.broadcastLocked()
	c.mu.Unlock()

	var errs []error
	if conn != nil {
		if err := conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			errs = append(errs, fmt.Errorf("queue: close connection: %w", err))
		}
	}
	c.wg.Wait()
	return errors.Join(errs...)
}

// topologyDeclarer is the channel surface queue-topology declaration needs. The
// concrete *amqp.Channel satisfies it; abstracting it lets a test fake record the
// declaration arguments, so the durability, priority, and dead-letter guarantees
// are pinned without a live broker.
type topologyDeclarer interface {
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
}

// declareTopology declares the durable, priority-enabled queue and, unless DLQs
// are disabled, its companion dead-letter queue. The main queue is declared with a
// dead-letter exchange (the default exchange) routing rejected messages by name to
// the DLQ, so a Nack(requeue=false) parks a poison message rather than discarding
// it. Both queues are durable so they and their persistent messages survive a
// broker restart; both carry x-max-priority so a dead-lettered message keeps its
// priority. Declaration is idempotent for matching arguments, so producer and
// consumers can each declare it.
//
// Because x-max-priority and the dead-letter arguments are fixed at declaration
// and cannot be changed on a live queue, a queue that predates DLQ routing must be
// recreated (a fresh environment or a version bump) for the new arguments to take;
// this is why DisableDLQ must be identical across every declarer.
func declareTopology(ch topologyDeclarer, cfg Config, dlqName string) error {
	priorityArgs := amqp.Table{"x-max-priority": int(cfg.MaxPriority)}

	mainArgs := amqp.Table{"x-max-priority": int(cfg.MaxPriority)}
	if !cfg.DisableDLQ {
		if _, err := ch.QueueDeclare(dlqName, true, false, false, false, priorityArgs); err != nil {
			return fmt.Errorf("queue: declare dlq %q: %w", dlqName, err)
		}
		// The default exchange routes a message by its routing key to the queue of
		// that name, so dead-lettering to routing key dlqName lands it in the DLQ
		// with no custom exchange to declare or bind.
		mainArgs["x-dead-letter-exchange"] = ""
		mainArgs["x-dead-letter-routing-key"] = dlqName
	}
	if _, err := ch.QueueDeclare(cfg.QueueName, true, false, false, false, mainArgs); err != nil {
		return fmt.Errorf("queue: declare %q: %w", cfg.QueueName, err)
	}
	return nil
}
