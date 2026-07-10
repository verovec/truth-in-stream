package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// controlledRunner is a LiveRunner whose event stream the test drives directly.
// Each Run creates a fresh buffered event channel and closes it when its context
// is canceled, modeling the real analyzer ending its stream on cancel.
type controlledRunner struct {
	mu       sync.Mutex
	events   chan LiveEvent
	runCalls int
	runErr   error
}

func (r *controlledRunner) Run(ctx context.Context, _ <-chan []byte) (<-chan LiveEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runErr != nil {
		return nil, r.runErr
	}
	r.runCalls++
	r.events = make(chan LiveEvent, 512)
	ev := r.events
	go func() {
		<-ctx.Done()
		close(ev)
	}()
	return ev, nil
}

func (r *controlledRunner) emit(e LiveEvent) {
	r.mu.Lock()
	ch := r.events
	r.mu.Unlock()
	ch <- e
}

func (r *controlledRunner) runs() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runCalls
}

// churnRunner is a LiveRunner safe for concurrent Run calls (each gets its own
// stream) that emits continuously until its context is canceled, so a churn test
// can spin up and tear down many sessions without racing on shared state.
type churnRunner struct{}

func (churnRunner) Run(ctx context.Context, _ <-chan []byte) (<-chan LiveEvent, error) {
	out := make(chan LiveEvent, 8)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case out <- subtitleEvent("x"):
			}
		}
	}()
	return out, nil
}

func hubTestLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func subtitleEvent(id string) LiveEvent {
	return LiveEvent{Kind: LiveEventSubtitle, ID: id, Segment: domain.Segment{Text: id}}
}

func recvMessage(t *testing.T, sub *TVSubscriber) TVViewerMessage {
	t.Helper()
	select {
	case m := <-sub.Messages():
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for viewer message")
		return TVViewerMessage{}
	}
}

func newHub(t *testing.T, runner LiveRunner) *TVHub {
	t.Helper()
	hub, err := NewTVHub(runner, hubTestLogger())
	if err != nil {
		t.Fatalf("NewTVHub: %v", err)
	}
	return hub
}

func TestNewTVHubRequiresAnalyzer(t *testing.T) {
	t.Parallel()
	if _, err := NewTVHub(nil, nil); err == nil {
		t.Fatal("NewTVHub(nil) should error")
	}
}

func TestTVHubFanOutAndLateJoinBacklog(t *testing.T) {
	t.Parallel()
	runner := &controlledRunner{}
	hub := newHub(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	viewerA := hub.Subscribe("chan1")
	defer viewerA.Close()

	pub, err := hub.Publish(ctx, "chan1")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	defer pub.Close()

	runner.emit(subtitleEvent("s1"))
	runner.emit(subtitleEvent("s2"))

	if m := recvMessage(t, viewerA); m.Event == nil || m.Event.ID != "s1" {
		t.Fatalf("viewerA first = %+v, want s1", m)
	}
	if m := recvMessage(t, viewerA); m.Event == nil || m.Event.ID != "s2" {
		t.Fatalf("viewerA second = %+v, want s2", m)
	}

	// A late joiner receives the recent backlog first.
	viewerB := hub.Subscribe("chan1")
	defer viewerB.Close()
	backlog := viewerB.Backlog()
	if len(backlog) != 2 || backlog[0].ID != "s1" || backlog[1].ID != "s2" {
		t.Fatalf("late-join backlog = %+v, want [s1 s2]", backlog)
	}

	// A subsequent event reaches both viewers.
	runner.emit(subtitleEvent("s3"))
	if m := recvMessage(t, viewerA); m.Event == nil || m.Event.ID != "s3" {
		t.Fatalf("viewerA third = %+v, want s3", m)
	}
	if m := recvMessage(t, viewerB); m.Event == nil || m.Event.ID != "s3" {
		t.Fatalf("viewerB first = %+v, want s3", m)
	}

	if runner.runs() != 1 {
		t.Fatalf("analyzer Run called %d times, want 1", runner.runs())
	}
}

func TestTVHubRejectsSecondPublisher(t *testing.T) {
	t.Parallel()
	runner := &controlledRunner{}
	hub := newHub(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pub1, err := hub.Publish(ctx, "chan1")
	if err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	defer pub1.Close()

	if _, err := hub.Publish(ctx, "chan1"); !errors.Is(err, ErrTVChannelBusy) {
		t.Fatalf("second Publish err = %v, want ErrTVChannelBusy", err)
	}
	// A different channel is unaffected.
	pub2, err := hub.Publish(ctx, "chan2")
	if err != nil {
		t.Fatalf("Publish chan2: %v", err)
	}
	defer pub2.Close()
}

func TestTVHubPublisherDisconnectTakesViewersOffAir(t *testing.T) {
	t.Parallel()
	runner := &controlledRunner{}
	hub := newHub(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	viewer := hub.Subscribe("chan1")
	defer viewer.Close()
	pub, err := hub.Publish(ctx, "chan1")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !hub.Live("chan1") {
		t.Fatal("channel should be live while a publisher is connected")
	}
	runner.emit(subtitleEvent("s1"))
	if m := recvMessage(t, viewer); m.Event == nil || m.Event.ID != "s1" {
		t.Fatalf("viewer event = %+v, want s1", m)
	}

	pub.Close()

	// The viewer receives an off-air message, and the channel reports not-live.
	m := recvMessage(t, viewer)
	if !m.OffAir {
		t.Fatalf("viewer message = %+v, want off_air", m)
	}
	if hub.Live("chan1") {
		t.Fatal("channel should not be live after the publisher disconnects")
	}
	// The message stream closes after off-air.
	select {
	case _, ok := <-viewer.Messages():
		if ok {
			t.Fatal("expected the viewer stream to be closed after off_air")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("viewer stream did not close after off_air")
	}
}

func TestTVHubDropsSlowSubscriber(t *testing.T) {
	t.Parallel()
	runner := &controlledRunner{}
	hub := newHub(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fast := hub.Subscribe("chan1")
	defer fast.Close()
	slow := hub.Subscribe("chan1") // never read, so its buffer fills
	defer slow.Close()

	pub, err := hub.Publish(ctx, "chan1")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	defer pub.Close()

	// Emit in lockstep with the fast reader (one read per emit) so the fast
	// subscriber never falls behind and is never itself dropped; each read
	// confirms the hub published the event to both subscribers. The slow
	// subscriber is never read, so its buffer fills and it is dropped.
	for i := 0; i < tvSubscriberBuffer+5; i++ {
		runner.emit(subtitleEvent("s"))
		if m := recvMessage(t, fast); m.Event == nil {
			t.Fatalf("fast subscriber message %d = %+v, want an event", i, m)
		}
	}

	// The slow subscriber, unread, was dropped: its stream is closed after at
	// most a buffer's worth of messages.
	drained := 0
	closed := false
	for !closed {
		select {
		case _, ok := <-slow.Messages():
			if !ok {
				closed = true
			} else {
				drained++
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("slow subscriber was not dropped (drained %d)", drained)
		}
	}
	if drained > tvSubscriberBuffer {
		t.Fatalf("slow subscriber buffered %d messages, want <= %d", drained, tvSubscriberBuffer)
	}
}

func TestTVHubBacklogIsBounded(t *testing.T) {
	t.Parallel()
	runner := &controlledRunner{}
	hub := newHub(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := hub.Subscribe("chan1")
	defer watcher.Close()
	pub, err := hub.Publish(ctx, "chan1")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	defer pub.Close()

	// Emit far more than the ring holds, in lockstep with a reader so the hub
	// has provably published every event by the end (and the reader is never
	// dropped for falling behind).
	total := tvRingSize + 50
	for i := 0; i < total; i++ {
		runner.emit(subtitleEvent("s"))
		if m := recvMessage(t, watcher); m.Event == nil {
			t.Fatalf("watcher message %d = %+v, want an event", i, m)
		}
	}

	// A viewer joining after many events gets only the bounded ring, so a
	// multi-hour session's memory stays bounded.
	late := hub.Subscribe("chan1")
	defer late.Close()
	if got := len(late.Backlog()); got != tvRingSize {
		t.Fatalf("late-join backlog = %d, want the bounded ring size %d", got, tvRingSize)
	}
}

// TestTVHubConcurrentChurnStaysConsistent hammers Subscribe/Publish/Close on a
// small set of shared channels so the room-lifecycle locking (get-or-create,
// registration, and gc all racing) is exercised under -race, then confirms a
// fresh session still delivers events - guarding against a subscriber or
// publisher being orphaned onto a gc'd room.
func TestTVHubConcurrentChurnStaysConsistent(t *testing.T) {
	t.Parallel()
	hub := newHub(t, churnRunner{})
	channels := []string{"a", "b", "c"}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch := channels[i%len(channels)]
			if i%2 == 0 {
				sub := hub.Subscribe(ch)
				select {
				case <-sub.Messages():
				default:
				}
				sub.Close()
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if pub, err := hub.Publish(ctx, ch); err == nil {
				pub.Close()
			}
		}(i)
	}
	wg.Wait()

	// The hub remains functional after the churn: a fresh publisher's events
	// reach a fresh subscriber on a clean channel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := hub.Subscribe("z")
	defer sub.Close()
	pub, err := hub.Publish(ctx, "z")
	if err != nil {
		t.Fatalf("Publish after churn: %v", err)
	}
	defer pub.Close()
	if m := recvMessage(t, sub); m.Event == nil {
		t.Fatal("fresh subscriber received no event after churn")
	}
}

func TestTVHubPublishAnalyzerErrorReleasesChannel(t *testing.T) {
	t.Parallel()
	runner := &controlledRunner{runErr: errors.New("boom")}
	hub := newHub(t, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := hub.Publish(ctx, "chan1"); err == nil {
		t.Fatal("Publish should surface the analyzer start error")
	}
	if hub.Live("chan1") {
		t.Fatal("a failed publish must leave the channel not-live")
	}
	// The channel is free for a later publisher once the analyzer recovers.
	runner.runErr = nil
	pub, err := hub.Publish(ctx, "chan1")
	if err != nil {
		t.Fatalf("Publish after recovery: %v", err)
	}
	defer pub.Close()
	if !hub.Live("chan1") {
		t.Fatal("channel should be live after a successful publish")
	}
}
