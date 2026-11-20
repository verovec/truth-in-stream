package queue

import (
	"context"
	"testing"
	"time"
)

// drainClosed reports whether ch is closed within timeout, returning the count of
// deliveries seen before it closed. A stream that stays open past timeout fails
// the wait so a test never blocks forever on a stream that should have exited.
func drainClosed(t *testing.T, ch <-chan Delivery, timeout time.Duration) (count int, closed bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return count, true
			}
			count++
		case <-deadline:
			return count, false
		}
	}
}

func TestWithIdleTimeoutDisabledReturnsSource(t *testing.T) {
	t.Parallel()
	src := make(chan Delivery)
	for _, idle := range []time.Duration{0, -time.Second} {
		if got := WithIdleTimeout(context.Background(), src, idle); got != (<-chan Delivery)(src) {
			t.Fatalf("idle %s: expected the source channel returned unchanged", idle)
		}
	}
}

func TestWithIdleTimeoutClosesOnEmptyQueue(t *testing.T) {
	t.Parallel()
	src := make(chan Delivery)
	out := WithIdleTimeout(context.Background(), src, 30*time.Millisecond)
	// No delivery ever arrives: the stream must close after the idle window.
	if _, closed := drainClosed(t, out, time.Second); !closed {
		t.Fatal("expected the stream to close after the idle window with an empty queue")
	}
}

func TestWithIdleTimeoutForwardsThenClosesOnIdle(t *testing.T) {
	t.Parallel()
	src := make(chan Delivery)
	out := WithIdleTimeout(context.Background(), src, 40*time.Millisecond)

	const n = 5
	go func() {
		for i := 0; i < n; i++ {
			src <- Delivery{Body: []byte{byte(i)}}
		}
		// Stop delivering and leave src open: only the idle window may close out.
	}()

	count, closed := drainClosed(t, out, 2*time.Second)
	if !closed {
		t.Fatal("expected the stream to close after the queue drained to idle")
	}
	if count != n {
		t.Fatalf("forwarded %d deliveries, want %d", count, n)
	}
}

func TestWithIdleTimeoutStaysOpenWhileDeliveriesArrive(t *testing.T) {
	t.Parallel()
	src := make(chan Delivery)
	out := WithIdleTimeout(context.Background(), src, 50*time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Deliver steadily for well over one idle window, faster than it: the stream
		// must not idle-exit while the queue is non-empty.
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for i := 0; i < 10; i++ {
			<-tick.C
			select {
			case src <- Delivery{Body: []byte{byte(i)}}:
			case <-time.After(time.Second):
				t.Errorf("forward %d blocked: stream idle-exited while deliveries were arriving", i)
				return
			}
		}
	}()

	// Consume every delivery the producer sends; the stream must remain open
	// throughout, so all ten are received before it is allowed to idle-close.
	got := 0
	timeout := time.After(2 * time.Second)
	for got < 10 {
		select {
		case _, ok := <-out:
			if !ok {
				t.Fatalf("stream closed early after %d deliveries", got)
			}
			got++
		case <-timeout:
			t.Fatalf("only received %d of 10 deliveries before timeout", got)
		}
	}
	<-done
}

func TestWithIdleTimeoutClosesWhenSourceCloses(t *testing.T) {
	t.Parallel()
	src := make(chan Delivery)
	out := WithIdleTimeout(context.Background(), src, time.Hour) // long window: only the src close ends it

	close(src)
	if _, closed := drainClosed(t, out, time.Second); !closed {
		t.Fatal("expected the stream to close when the source closes, before the idle window")
	}
}

func TestWithIdleTimeoutClosesOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	src := make(chan Delivery)
	out := WithIdleTimeout(ctx, src, time.Hour)

	cancel()
	if _, closed := drainClosed(t, out, time.Second); !closed {
		t.Fatal("expected the stream to close when the context is canceled")
	}
}
