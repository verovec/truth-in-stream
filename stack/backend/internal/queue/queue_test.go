package queue

import (
	"context"
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
	c := &Client{queueName: "q", maxPriority: 9}
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

// TestDeclareQueueDeclaresDurablePriorityQueue pins property 1's queue half: the
// queue is declared durable (so it and its persistent messages survive a broker
// restart), at the requested priority ceiling, and as a shared surviving work
// queue (not auto-delete, exclusive, or no-wait). A fake declarer records the
// arguments, so CI proves durability without a live broker.
func TestDeclareQueueDeclaresDurablePriorityQueue(t *testing.T) {
	t.Parallel()

	fd := &fakeDeclarer{}
	if err := declareQueue(fd, "embedding.jobs.v1", 9); err != nil {
		t.Fatalf("declareQueue() error = %v", err)
	}
	if !fd.called {
		t.Fatal("declareQueue did not declare the queue")
	}
	if fd.name != "embedding.jobs.v1" {
		t.Fatalf("declared queue name = %q, want %q", fd.name, "embedding.jobs.v1")
	}
	if !fd.durable {
		t.Fatal("queue declared non-durable; a broker restart would drop the queue and its persistent messages")
	}
	if fd.autoDelete || fd.exclusive || fd.noWait {
		t.Fatalf("queue declared autoDelete=%v exclusive=%v noWait=%v, want all false (a shared work queue that outlives any single consumer)", fd.autoDelete, fd.exclusive, fd.noWait)
	}
	if got := fd.args["x-max-priority"]; got != int(9) {
		t.Fatalf("x-max-priority = %v, want 9 (priority delivery at the declared ceiling)", got)
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

// fakeDeclarer records the arguments a queue declaration is issued with, standing
// in for an AMQP channel so declareQueue's durability and priority arguments are
// asserted without a live broker.
type fakeDeclarer struct {
	called     bool
	name       string
	durable    bool
	autoDelete bool
	exclusive  bool
	noWait     bool
	args       amqp.Table
	err        error
}

func (f *fakeDeclarer) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	f.called = true
	f.name, f.durable, f.autoDelete, f.exclusive, f.noWait, f.args = name, durable, autoDelete, exclusive, noWait, args
	return amqp.Queue{Name: name}, f.err
}

var _ queueDeclarer = (*fakeDeclarer)(nil)

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
