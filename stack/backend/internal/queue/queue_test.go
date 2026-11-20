package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "empty url", cfg: Config{QueueName: "q", Version: "1", MaxPriority: 10}},
		{name: "empty queue name", cfg: Config{URL: "amqp://localhost", Version: "1", MaxPriority: 10}},
		{name: "empty version", cfg: Config{URL: "amqp://localhost", QueueName: "q", MaxPriority: 10}},
		{name: "zero max priority", cfg: Config{URL: "amqp://localhost", QueueName: "q", Version: "1", MaxPriority: 0}},
		{name: "negative prefetch", cfg: Config{URL: "amqp://localhost", QueueName: "q", Version: "1", MaxPriority: 10, Prefetch: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Validation precedes dialing, so an invalid config never reaches a
			// broker: a non-nil error with no client is the whole contract.
			client, err := New(tc.cfg)
			if err == nil {
				t.Fatalf("New(%+v) = nil error, want validation error", tc.cfg)
			}
			if client != nil {
				t.Fatalf("New(%+v) returned a client on error", tc.cfg)
			}
		})
	}
}

func TestPublishRejectsPriorityAboveMaximum(t *testing.T) {
	t.Parallel()

	// Priority is validated before the channel is touched, so a bare Client with
	// the maximum set exercises the guard without a broker.
	c := &Client{cfg: Config{QueueName: "q", MaxPriority: 9}}
	err := c.Publish(t.Context(), Message{Body: []byte("x"), Priority: 10})
	if err == nil {
		t.Fatal("Publish with priority above the queue maximum = nil error, want rejection")
	}
}

// TestPersistentPublishingIsPersistentAndVersioned pins property 1's publish
// half: every message leaves the process with a persistent delivery mode (so a
// broker restart keeps it) and the active schema version in its headers, carrying
// the caller's priority and body unchanged. It runs without a broker, so CI proves
// persistence even though the broker round-trip skips.
func TestPersistentPublishingIsPersistentAndVersioned(t *testing.T) {
	t.Parallel()

	pub := persistentPublishing("3", Message{Body: []byte("payload"), Priority: 7})
	if pub.DeliveryMode != amqp.Persistent {
		t.Fatalf("DeliveryMode = %d, want %d (amqp.Persistent); a transient message is dropped on a broker restart", pub.DeliveryMode, amqp.Persistent)
	}
	if pub.Priority != 7 {
		t.Fatalf("Priority = %d, want 7 (the caller's priority is preserved)", pub.Priority)
	}
	if got, _ := pub.Headers[versionHeader].(string); got != "3" {
		t.Fatalf("version header = %q, want %q", got, "3")
	}
	if string(pub.Body) != "payload" {
		t.Fatalf("Body = %q, want %q (the caller's body is untouched)", pub.Body, "payload")
	}
}

// TestDeclareTopologyDeclaresDurablePriorityQueueAndDLQ pins property 1's queue
// half plus the dead-letter half: the DLQ is declared first (durable, priority,
// no dead-letter of its own so a poison message cannot loop), then the main queue
// durable at the priority ceiling, as a shared surviving work queue, with a
// dead-letter exchange routing rejected messages by name to the DLQ. A fake
// declarer records every declaration, so CI proves the topology without a live
// broker.
func TestDeclareTopologyDeclaresDurablePriorityQueueAndDLQ(t *testing.T) {
	t.Parallel()

	fd := &fakeDeclarer{}
	cfg := Config{QueueName: "embedding.jobs.v1", Version: "1", MaxPriority: 9}
	if err := declareTopology(fd, cfg, deriveDLQName(cfg.QueueName, cfg.Version)); err != nil {
		t.Fatalf("declareTopology() error = %v", err)
	}
	if len(fd.calls) != 2 {
		t.Fatalf("declareTopology issued %d declarations, want 2 (dlq then main)", len(fd.calls))
	}

	dlq := fd.calls[0]
	if dlq.name != "embedding.jobs.dlq.v1" {
		t.Fatalf("dlq name = %q, want %q", dlq.name, "embedding.jobs.dlq.v1")
	}
	if !dlq.durable {
		t.Fatal("dlq declared non-durable; a broker restart would drop parked poison messages")
	}
	if _, hasDLX := dlq.args["x-dead-letter-exchange"]; hasDLX {
		t.Fatal("dlq declared with its own dead-letter exchange; a poison message could loop")
	}
	if got := dlq.args["x-max-priority"]; got != int(9) {
		t.Fatalf("dlq x-max-priority = %v, want 9 (a dead-lettered message keeps its priority)", got)
	}

	main := fd.calls[1]
	if main.name != "embedding.jobs.v1" {
		t.Fatalf("declared queue name = %q, want %q", main.name, "embedding.jobs.v1")
	}
	if !main.durable {
		t.Fatal("queue declared non-durable; a broker restart would drop the queue and its persistent messages")
	}
	if main.autoDelete || main.exclusive || main.noWait {
		t.Fatalf("queue declared autoDelete=%v exclusive=%v noWait=%v, want all false (a shared work queue that outlives any single consumer)", main.autoDelete, main.exclusive, main.noWait)
	}
	if got := main.args["x-max-priority"]; got != int(9) {
		t.Fatalf("x-max-priority = %v, want 9 (priority delivery at the declared ceiling)", got)
	}
	if got := main.args["x-dead-letter-exchange"]; got != "" {
		t.Fatalf("x-dead-letter-exchange = %v, want \"\" (dead-letter via the default exchange)", got)
	}
	if got := main.args["x-dead-letter-routing-key"]; got != "embedding.jobs.dlq.v1" {
		t.Fatalf("x-dead-letter-routing-key = %v, want the dlq name (routes a rejected message to the DLQ)", got)
	}
}

// TestDeclareTopologyWithoutDLQ pins the opt-out: with DisableDLQ set no DLQ is
// declared and the main queue carries no dead-letter arguments, so an operator can
// turn the feature off consistently across every declarer.
func TestDeclareTopologyWithoutDLQ(t *testing.T) {
	t.Parallel()

	fd := &fakeDeclarer{}
	cfg := Config{QueueName: "embedding.jobs.v1", Version: "1", MaxPriority: 9, DisableDLQ: true}
	if err := declareTopology(fd, cfg, deriveDLQName(cfg.QueueName, cfg.Version)); err != nil {
		t.Fatalf("declareTopology() error = %v", err)
	}
	if len(fd.calls) != 1 {
		t.Fatalf("declareTopology issued %d declarations, want 1 (main only, no DLQ)", len(fd.calls))
	}
	main := fd.calls[0]
	if _, hasDLX := main.args["x-dead-letter-exchange"]; hasDLX {
		t.Fatal("main queue carries a dead-letter exchange with DLQs disabled")
	}
}

// TestRetryPublishClassifiesErrors pins the publish-retry decision without a
// broker: a canceled context is terminal, a non-connection error is terminal, and
// a closed-connection error is retryable (the Publish loop then re-checks the live
// channel via currentPubCh rather than waiting here for a possibly-already-done
// reconnect).
func TestRetryPublishClassifiesErrors(t *testing.T) {
	t.Parallel()

	t.Run("canceled context is terminal", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cause := errors.New("boom")
		c := &Client{}
		if retry, err := c.retryPublish(ctx, cause); retry || !errors.Is(err, cause) {
			t.Fatalf("retryPublish(canceled) = (%v, %v), want (false, boom)", retry, err)
		}
	})

	t.Run("non-connection error is terminal", func(t *testing.T) {
		t.Parallel()
		cause := errors.New("queue full")
		c := &Client{}
		if retry, err := c.retryPublish(context.Background(), cause); retry || !errors.Is(err, cause) {
			t.Fatalf("retryPublish(non-conn) = (%v, %v), want (false, queue full)", retry, err)
		}
	})

	t.Run("closed connection is retryable", func(t *testing.T) {
		t.Parallel()
		c := &Client{}
		if retry, err := c.retryPublish(context.Background(), amqp.ErrClosed); !retry || err != nil {
			t.Fatalf("retryPublish(ErrClosed) = (%v, %v), want (true, nil)", retry, err)
		}
	})
}

// TestCurrentPubChReturnsErrClosedOnClosedClient proves the terminal case that used
// to live in retryPublish now stops a publish loop: once the client is closed,
// currentPubCh returns ErrClosed instead of blocking for a reconnect that will
// never come.
func TestCurrentPubChReturnsErrClosedOnClosedClient(t *testing.T) {
	t.Parallel()
	c := &Client{closed: true, stateCh: make(chan struct{}), stopCh: make(chan struct{})}
	if ch, err := c.currentPubCh(context.Background()); ch != nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("currentPubCh(closed) = (%v, %v), want (nil, ErrClosed)", ch, err)
	}
}

// TestDeriveDLQName pins the companion-queue naming: the DLQ keeps the queue's
// version suffix, and an unexpected name falls back to a unique .dlq suffix.
func TestDeriveDLQName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		queueName, version, want string
	}{
		{"embedding.jobs.v1", "1", "embedding.jobs.dlq.v1"},
		{"scrutins.votes.v2", "2", "scrutins.votes.dlq.v2"},
		{"factcheck.claims.v10", "10", "factcheck.claims.dlq.v10"},
		{"noversionsuffix", "1", "noversionsuffix.dlq"},
	}
	for _, tc := range tests {
		if got := deriveDLQName(tc.queueName, tc.version); got != tc.want {
			t.Errorf("deriveDLQName(%q, %q) = %q, want %q", tc.queueName, tc.version, got, tc.want)
		}
	}
}

// TestBackoffWindow pins the reconnect backoff: the ceiling doubles per attempt
// from the minimum, caps at the maximum, and the jitter floor is half the ceiling,
// so a fleet redialing after one broker restart spreads out instead of thundering.
func TestBackoffWindow(t *testing.T) {
	t.Parallel()

	const minB, maxB = 250 * time.Millisecond, 30 * time.Second
	tests := []struct {
		attempt        int
		wantLo, wantHi time.Duration
	}{
		{0, 125 * time.Millisecond, 250 * time.Millisecond},
		{1, 250 * time.Millisecond, 500 * time.Millisecond},
		{2, 500 * time.Millisecond, time.Second},
		{100, maxB / 2, maxB}, // saturates at the cap, never overflows
	}
	for _, tc := range tests {
		lo, hi := backoffWindow(tc.attempt, minB, maxB)
		if lo != tc.wantLo || hi != tc.wantHi {
			t.Errorf("backoffWindow(%d) = [%v, %v], want [%v, %v]", tc.attempt, lo, hi, tc.wantLo, tc.wantHi)
		}
	}
}

func TestDeliveryAckDelegatesToAcknowledger(t *testing.T) {
	t.Parallel()

	acker := &fakeAcker{}
	d := Delivery{Body: []byte("body"), Priority: 3, acker: acker, tag: 42}
	if err := d.Ack(); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	if !acker.acked || acker.ackTag != 42 || acker.ackMultiple {
		t.Fatalf("Ack() delegated as {acked:%v tag:%d multiple:%v}, want {true 42 false}", acker.acked, acker.ackTag, acker.ackMultiple)
	}
}

func TestDeliveryNackDelegatesRequeue(t *testing.T) {
	t.Parallel()

	for _, requeue := range []bool{true, false} {
		acker := &fakeAcker{}
		d := Delivery{acker: acker, tag: 7}
		if err := d.Nack(requeue); err != nil {
			t.Fatalf("Nack(%v) error = %v", requeue, err)
		}
		if !acker.nacked || acker.nackTag != 7 || acker.nackMultiple || acker.nackRequeue != requeue {
			t.Fatalf("Nack(%v) delegated requeue=%v tag=%d multiple=%v", requeue, acker.nackRequeue, acker.nackTag, acker.nackMultiple)
		}
	}
}

func TestDeliveryWithoutAcknowledgerErrors(t *testing.T) {
	t.Parallel()

	var d Delivery
	if err := d.Ack(); err == nil {
		t.Fatal("Ack() on a zero Delivery = nil error, want error")
	}
	if err := d.Nack(true); err == nil {
		t.Fatal("Nack() on a zero Delivery = nil error, want error")
	}
}

// TestClientRoundTrip publishes prioritized, persistent messages and consumes
// them with manual ack against a real broker. It is the integration proof of
// the acceptance criteria and skips without TEST_RABBITMQ_URL, mirroring the
// store integration tests' TEST_DATABASE_URL gate.
func TestClientRoundTrip(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("set TEST_RABBITMQ_URL to run the broker round-trip")
	}

	ctx := t.Context()
	// A unique queue per run keeps repeated runs and parallel CI jobs isolated.
	queueName := "test.embedding.jobs." + time.Now().Format("20060102150405.000")
	client, err := New(Config{URL: url, QueueName: queueName, Version: "1", MaxPriority: 10, Prefetch: 1})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		cleanupQueue(t, url, queueName)
	})

	// Publish low priority first, then high: a priority queue must deliver the
	// high-priority message before the low-priority one regardless of order.
	if err := client.Publish(ctx, Message{Body: []byte("low"), Priority: 1}); err != nil {
		t.Fatalf("Publish(low) error = %v", err)
	}
	if err := client.Publish(ctx, Message{Body: []byte("high"), Priority: 9}); err != nil {
		t.Fatalf("Publish(high) error = %v", err)
	}

	deliveries, err := client.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}

	got := make([]string, 0, 2)
	for range 2 {
		select {
		case d := <-deliveries:
			got = append(got, string(d.Body))
			if d.Version != "1" {
				t.Fatalf("delivery version = %q, want %q (publisher stamps the active version)", d.Version, "1")
			}
			if err := d.Ack(); err != nil {
				t.Fatalf("Ack() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for delivery; got %v so far", got)
		}
	}

	if want := []string{"high", "low"}; !slices.Equal(got, want) {
		t.Fatalf("delivery order = %v, want %v (priority ordering)", got, want)
	}
}

// TestClientCloseEndsConsumerWithoutCancel proves Close does not deadlock when a
// consumer's context is never canceled: closing the connection ends the
// consumer regardless. It skips without TEST_RABBITMQ_URL.
func TestClientCloseEndsConsumerWithoutCancel(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("set TEST_RABBITMQ_URL to run the broker round-trip")
	}

	queueName := "test.embedding.jobs." + time.Now().Format("20060102150405.000") + ".close"
	client, err := New(Config{URL: url, QueueName: queueName, Version: "1", MaxPriority: 10, Prefetch: 1})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { cleanupQueue(t, url, queueName) })

	// A context that is never canceled: the consumer would block forever if
	// Close waited on it before closing the connection.
	if _, err := client.Consume(context.Background()); err != nil {
		t.Fatalf("Consume() error = %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- client.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return; it deadlocked waiting on an uncanceled consumer")
	}
}

// TestClientConcurrentPublish exercises the lock-release-before-confirm path:
// many goroutines publish at once and every message must still be confirmed and
// delivered. It guards against the publish frame and its confirmation racing. It
// skips without TEST_RABBITMQ_URL.
func TestClientConcurrentPublish(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("set TEST_RABBITMQ_URL to run the broker round-trip")
	}

	ctx := t.Context()
	queueName := "test.embedding.jobs." + time.Now().Format("20060102150405.000") + ".concurrent"
	client, err := New(Config{URL: url, QueueName: queueName, Version: "1", MaxPriority: 10, Prefetch: 10})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		cleanupQueue(t, url, queueName)
	})

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Publish(ctx, Message{Body: fmt.Appendf(nil, "msg-%d", i), Priority: 5}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Publish error = %v", err)
	}

	deliveries, err := client.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	seen := make(map[string]bool, n)
	for range n {
		select {
		case d := <-deliveries:
			seen[string(d.Body)] = true
			if err := d.Ack(); err != nil {
				t.Fatalf("Ack() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out; received %d of %d", len(seen), n)
		}
	}
	if len(seen) != n {
		t.Fatalf("received %d distinct messages, want %d", len(seen), n)
	}
}

// TestClientResumesAfterConnectionDrop is the reconnect acceptance proof: a
// consumer draining the queue survives an involuntary connection drop (a stand-in
// for a broker restart) without a process restart, and a publish issued while the
// connection is down completes once the supervisor reconnects. It skips without
// TEST_RABBITMQ_URL.
func TestClientResumesAfterConnectionDrop(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("set TEST_RABBITMQ_URL to run the broker round-trip")
	}

	ctx := t.Context()
	queueName := "test.embedding.jobs." + time.Now().Format("20060102150405.000") + ".reconnect"
	client, err := New(Config{URL: url, QueueName: queueName, Version: "1", MaxPriority: 10, Prefetch: 1, MinBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		cleanupQueue(t, url, queueName)
		cleanupQueue(t, url, deriveDLQName(queueName, "1"))
	})

	deliveries, err := client.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}

	// Drain one message on the original connection.
	if err := client.Publish(ctx, Message{Body: []byte("before"), Priority: 5}); err != nil {
		t.Fatalf("Publish(before) error = %v", err)
	}
	recv(t, deliveries, "before")

	// Force the connection down; the supervisor must redial transparently.
	dropConnForTest(t, client)

	// A publish across the outage completes once the client reconnects, and the
	// same Consume stream keeps delivering without having been re-created.
	if err := client.Publish(ctx, Message{Body: []byte("after"), Priority: 5}); err != nil {
		t.Fatalf("Publish(after) across reconnect error = %v", err)
	}
	recv(t, deliveries, "after")
}

// TestClientResumesAfterPublishChannelClose proves a publish-channel-only failure
// (a channel-level exception while the TCP connection stays healthy) is healed
// without a full reconnect: the supervisor replaces the channel and a subsequent
// Publish succeeds rather than stalling. It skips without TEST_RABBITMQ_URL.
func TestClientResumesAfterPublishChannelClose(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("set TEST_RABBITMQ_URL to run the broker round-trip")
	}

	ctx := t.Context()
	queueName := "test.embedding.jobs." + time.Now().Format("20060102150405.000") + ".chanclose"
	client, err := New(Config{URL: url, QueueName: queueName, Version: "1", MaxPriority: 10, Prefetch: 1, MinBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		cleanupQueue(t, url, queueName)
		cleanupQueue(t, url, deriveDLQName(queueName, "1"))
	})

	deliveries, err := client.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if err := client.Publish(ctx, Message{Body: []byte("before"), Priority: 5}); err != nil {
		t.Fatalf("Publish(before) error = %v", err)
	}
	recv(t, deliveries, "before")

	// Close only the publish channel; the connection stays up. The supervisor must
	// replace the channel so the next publish does not hang.
	closePubChannelForTest(t, client)

	if err := client.Publish(ctx, Message{Body: []byte("after"), Priority: 5}); err != nil {
		t.Fatalf("Publish(after) after a channel-only close error = %v", err)
	}
	recv(t, deliveries, "after")
}

// TestClientRejectDeadLettersToDLQ proves nothing is acked-and-lost: a delivery
// the consumer rejects with Nack(false) is dead-lettered to the companion DLQ with
// its original body, ready to inspect and replay. It skips without
// TEST_RABBITMQ_URL.
func TestClientRejectDeadLettersToDLQ(t *testing.T) {
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("set TEST_RABBITMQ_URL to run the broker round-trip")
	}

	ctx := t.Context()
	queueName := "test.embedding.jobs." + time.Now().Format("20060102150405.000") + ".dlq"
	dlqName := deriveDLQName(queueName, "1")
	client, err := New(Config{URL: url, QueueName: queueName, Version: "1", MaxPriority: 10, Prefetch: 1})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		cleanupQueue(t, url, queueName)
		cleanupQueue(t, url, dlqName)
	})

	if err := client.Publish(ctx, Message{Body: []byte("poison"), Priority: 5}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	deliveries, err := client.Consume(ctx)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	select {
	case d := <-deliveries:
		if err := d.Nack(false); err != nil {
			t.Fatalf("Nack(false) error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the poison delivery")
	}

	// The rejected message must land in the DLQ, not vanish.
	body := getFromQueue(t, url, dlqName)
	if body != "poison" {
		t.Fatalf("DLQ body = %q, want %q (a rejected message is parked, not dropped)", body, "poison")
	}
}

// recv reads the next delivery, asserts its body, and acks it, failing on timeout.
func recv(t *testing.T, deliveries <-chan Delivery, want string) {
	t.Helper()
	select {
	case d := <-deliveries:
		if string(d.Body) != want {
			t.Fatalf("delivery body = %q, want %q", d.Body, want)
		}
		if err := d.Ack(); err != nil {
			t.Fatalf("Ack() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for delivery %q", want)
	}
}

// dropConnForTest closes the client's live connection, standing in for a broker
// restart: the supervisor sees NotifyClose fire and must redial on its own.
func dropConnForTest(t *testing.T, c *Client) {
	t.Helper()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		t.Fatal("client has no live connection to drop")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("dropping connection: %v", err)
	}
}

// closePubChannelForTest closes only the client's publish channel, leaving the
// connection open, standing in for a channel-level exception the supervisor must
// heal by replacing the channel rather than reconnecting.
func closePubChannelForTest(t *testing.T, c *Client) {
	t.Helper()
	c.mu.Lock()
	ch := c.pubCh
	c.mu.Unlock()
	if ch == nil {
		t.Fatal("client has no live publish channel to close")
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("closing publish channel: %v", err)
	}
}

// getFromQueue synchronously fetches one message body from a queue, polling
// briefly because a dead-letter hop is asynchronous.
func getFromQueue(t *testing.T, url, queueName string) string {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("get dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	defer func() { _ = ch.Close() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msg, ok, err := ch.Get(queueName, true)
		if err != nil {
			t.Fatalf("get from %q: %v", queueName, err)
		}
		if ok {
			return string(msg.Body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a message in %q", queueName)
	return ""
}

// cleanupQueue removes the per-test queue so a broker reused across runs does
// not accumulate them.
func cleanupQueue(t *testing.T, url, queueName string) {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Logf("cleanup dial: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Logf("cleanup channel: %v", err)
		return
	}
	defer func() { _ = ch.Close() }()
	if _, err := ch.QueueDelete(queueName, false, false, false); err != nil {
		t.Logf("cleanup delete queue: %v", err)
	}
}

// declaration records the arguments one queue declaration was issued with.
type declaration struct {
	name       string
	durable    bool
	autoDelete bool
	exclusive  bool
	noWait     bool
	args       amqp.Table
}

// fakeDeclarer records every queue declaration issued against it, standing in for
// an AMQP channel so declareTopology's durability, priority, and dead-letter
// arguments are asserted without a live broker.
type fakeDeclarer struct {
	calls []declaration
	err   error
}

func (f *fakeDeclarer) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	f.calls = append(f.calls, declaration{name, durable, autoDelete, exclusive, noWait, args})
	return amqp.Queue{Name: name}, f.err
}

var _ topologyDeclarer = (*fakeDeclarer)(nil)

// fakeAcker records the acknowledgement calls a Delivery delegates to it,
// standing in for an AMQP channel in unit tests.
type fakeAcker struct {
	acked       bool
	ackTag      uint64
	ackMultiple bool

	nacked       bool
	nackTag      uint64
	nackMultiple bool
	nackRequeue  bool

	err error
}

func (f *fakeAcker) Ack(tag uint64, multiple bool) error {
	f.acked, f.ackTag, f.ackMultiple = true, tag, multiple
	return f.err
}

func (f *fakeAcker) Nack(tag uint64, multiple, requeue bool) error {
	f.nacked, f.nackTag, f.nackMultiple, f.nackRequeue = true, tag, multiple, requeue
	return f.err
}

func (f *fakeAcker) Reject(_ uint64, _ bool) error {
	return f.err
}

var _ amqp.Acknowledger = (*fakeAcker)(nil)
