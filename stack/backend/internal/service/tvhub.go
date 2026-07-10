package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// ErrTVChannelBusy is returned by TVHub.Publish when a channel already has a
// connected publisher feed. One capture feed per channel is the invariant: a
// second feed is rejected so two capture processes cannot analyze the same
// channel into one session.
var ErrTVChannelBusy = errors.New("tv hub: channel already has a publisher")

const (
	// tvRingSize bounds the late-join backlog: the most recent events a viewer
	// receives on connect. A channel stream is unbounded, so the ring is what
	// keeps a multi-hour session's memory bounded (the per-video replay cache is
	// deliberately not used here).
	tvRingSize = 200
	// tvSubscriberBuffer bounds one viewer's pending-event queue. A viewer that
	// falls this far behind is disconnected rather than allowed to block the
	// session or grow without bound.
	tvSubscriberBuffer = 64
	// tvAudioBuffer bounds inbound PCM frames queued between the feed socket and
	// the analyzer, absorbing transient analysis stalls while bounding memory.
	tvAudioBuffer = 32
)

// LiveRunner is the analyzer port the hub drives: PCM bytes in, fact-check
// events out. *LiveAnalyzer satisfies it. The hub runs one instance per channel
// and never shares analyzer state across channels.
type LiveRunner interface {
	Run(ctx context.Context, audio <-chan []byte) (<-chan LiveEvent, error)
}

// TVViewerMessage is one item delivered to a viewer: either a live fact-check
// event or an off-air signal. OffAir is set exactly once, when the publisher
// disconnects and the session ends, so a viewer can render an "off air" state;
// it is distinct from the channel simply closing (a slow viewer being dropped).
type TVViewerMessage struct {
	Event  *LiveEvent
	OffAir bool
}

// TVHub runs one live analysis session per TV channel and fans its events out to
// any number of viewers. A channel's "room" outlives individual sessions so a
// viewer can subscribe to an idle channel and start receiving events when a
// publisher connects. The hub holds no transport types: the WebSocket sockets
// live entirely in the handler layer.
type TVHub struct {
	analyzer LiveRunner
	logger   *slog.Logger

	mu    sync.Mutex
	rooms map[string]*channelRoom
}

// NewTVHub builds a hub over the given analyzer. It fails fast on a nil
// analyzer; logger defaults to slog.Default.
func NewTVHub(analyzer LiveRunner, logger *slog.Logger) (*TVHub, error) {
	if analyzer == nil {
		return nil, errors.New("tv hub: analyzer is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &TVHub{analyzer: analyzer, logger: logger, rooms: map[string]*channelRoom{}}, nil
}

// channelRoom is a channel's shared state: the current session's recent-event
// ring, the set of subscribed viewers, and whether a publisher is connected.
type channelRoom struct {
	mu        sync.Mutex
	subs      map[*subscriber]struct{}
	ring      []LiveEvent
	publisher bool
	cancel    context.CancelFunc
}

type subscriber struct {
	ch chan TVViewerMessage
}

// roomLocked returns the channel's room, creating it on first use. The caller
// MUST hold h.mu across both the lookup and the room mutation that follows
// (registering a publisher or subscriber): gcRoom deletes an empty room while
// holding h.mu, so a caller that released h.mu between the lookup and its
// mutation could attach to a room gcRoom then removes from the map, orphaning
// it. Holding h.mu across both serializes against gcRoom and keeps lock order
// hub-then-room.
func (h *TVHub) roomLocked(channelID string) *channelRoom {
	room, ok := h.rooms[channelID]
	if !ok {
		room = &channelRoom{subs: map[*subscriber]struct{}{}}
		h.rooms[channelID] = room
	}
	return room
}

// gcRoom removes a channel's room once it has no publisher and no subscribers,
// so idle channels do not accumulate. Lock order is always hub then room.
func (h *TVHub) gcRoom(channelID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[channelID]
	if !ok {
		return
	}
	room.mu.Lock()
	empty := !room.publisher && len(room.subs) == 0
	room.mu.Unlock()
	if empty {
		delete(h.rooms, channelID)
	}
}

// Live reports whether the channel currently has a connected publisher feed. The
// channel-list handler uses it to enrich each channel's live status.
func (h *TVHub) Live(channelID string) bool {
	h.mu.Lock()
	room, ok := h.rooms[channelID]
	h.mu.Unlock()
	if !ok {
		return false
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	return room.publisher
}

// TVPublisher is a connected capture feed. The feed handler pumps PCM frames
// into it and calls Close when the feed ends.
type TVPublisher struct {
	audio  chan []byte
	ctx    context.Context
	cancel context.CancelFunc
}

// Publish registers the single publisher for a channel and starts its analysis
// session. It returns ErrTVChannelBusy if a publisher already holds the channel.
// The returned publisher's session is bound to ctx: it ends when ctx is
// canceled or Close is called, whichever comes first.
func (h *TVHub) Publish(ctx context.Context, channelID string) (*TVPublisher, error) {
	// Hold h.mu across the lookup and the publisher-flag set so gcRoom cannot
	// delete the room between them; once publisher is true the room is safe from
	// gc, so h.mu is released before the (potentially slow) analyzer start.
	h.mu.Lock()
	room := h.roomLocked(channelID)
	room.mu.Lock()
	if room.publisher {
		room.mu.Unlock()
		h.mu.Unlock()
		return nil, ErrTVChannelBusy
	}
	room.publisher = true
	room.ring = nil // a fresh session starts with an empty backlog
	sessCtx, cancel := context.WithCancel(ctx)
	room.cancel = cancel
	room.mu.Unlock()
	h.mu.Unlock()

	audio := make(chan []byte, tvAudioBuffer)
	events, err := h.analyzer.Run(sessCtx, audio)
	if err != nil {
		room.mu.Lock()
		room.publisher = false
		room.cancel = nil
		room.mu.Unlock()
		cancel()
		h.gcRoom(channelID)
		return nil, fmt.Errorf("tv hub: start analyzer for channel %s: %w", channelID, err)
	}

	go h.broadcast(channelID, room, events)
	return &TVPublisher{audio: audio, ctx: sessCtx, cancel: cancel}, nil
}

// Feed forwards one PCM frame to the analyzer, applying backpressure while the
// buffer is full and returning immediately once the session ends (so a caller is
// never wedged writing to a stopped analyzer).
func (p *TVPublisher) Feed(frame []byte) {
	select {
	case <-p.ctx.Done():
	case p.audio <- frame:
	}
}

// Close ends the publisher's session. The analyzer stops, viewers receive an
// off-air message, and the channel reports not-live. It is idempotent.
func (p *TVPublisher) Close() {
	p.cancel()
}

// broadcast pumps the analyzer's events into the room until the stream ends
// (analyzer stopped, session canceled, or provider ended), then tears the
// session down: viewers get off-air and the channel returns to idle.
func (h *TVHub) broadcast(channelID string, room *channelRoom, events <-chan LiveEvent) {
	for ev := range events {
		room.publish(ev)
	}
	room.endSession()
	h.gcRoom(channelID)
}

// publish appends an event to the bounded ring and fans it out to every
// subscriber. A subscriber whose buffer is full is dropped (disconnected)
// rather than allowed to block the session.
func (r *channelRoom) publish(ev LiveEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ring = append(r.ring, ev)
	if len(r.ring) > tvRingSize {
		// Slide the window down in place, reusing the backing array rather than
		// reallocating a fresh slice on every event of a long-running session.
		copy(r.ring, r.ring[len(r.ring)-tvRingSize:])
		r.ring = r.ring[:tvRingSize]
	}
	msg := TVViewerMessage{Event: &ev}
	for sub := range r.subs {
		select {
		case sub.ch <- msg:
		default:
			r.removeSub(sub)
		}
	}
}

// endSession marks the channel idle, cancels the session, and delivers a final
// off-air message to every viewer before disconnecting them.
func (r *channelRoom) endSession() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publisher = false
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.ring = nil
	for sub := range r.subs {
		select {
		case sub.ch <- TVViewerMessage{OffAir: true}:
		default:
		}
		r.removeSub(sub)
	}
}

// removeSub detaches a subscriber and closes its channel exactly once. It must
// be called under r.mu; the membership check makes a repeated remove a no-op, so
// a slow-drop, a session teardown, and a viewer's own Close never double-close.
func (r *channelRoom) removeSub(sub *subscriber) {
	if _, ok := r.subs[sub]; !ok {
		return
	}
	delete(r.subs, sub)
	close(sub.ch)
}

// TVSubscriber is one viewer's attachment to a channel: the backlog to send
// first, then a stream of live messages.
type TVSubscriber struct {
	hub       *TVHub
	channelID string
	room      *channelRoom
	sub       *subscriber
	backlog   []LiveEvent
}

// Subscribe attaches a viewer to a channel. It returns the current recent-event
// backlog (empty when the channel is idle) plus a message stream. Attaching and
// snapshotting the backlog happen atomically, so no event is missed or
// duplicated across the join.
func (h *TVHub) Subscribe(channelID string) *TVSubscriber {
	// Hold h.mu across the lookup and the subscriber registration so gcRoom
	// cannot delete the room between them and orphan the new viewer.
	h.mu.Lock()
	defer h.mu.Unlock()
	room := h.roomLocked(channelID)
	room.mu.Lock()
	defer room.mu.Unlock()
	sub := &subscriber{ch: make(chan TVViewerMessage, tvSubscriberBuffer)}
	room.subs[sub] = struct{}{}
	backlog := append([]LiveEvent(nil), room.ring...)
	return &TVSubscriber{hub: h, channelID: channelID, room: room, sub: sub, backlog: backlog}
}

// Backlog returns the recent events captured before this viewer joined, to be
// sent ahead of the live stream.
func (s *TVSubscriber) Backlog() []LiveEvent {
	return s.backlog
}

// Messages is the viewer's live stream. It closes when the viewer is dropped or
// the session ends; an OffAir message precedes the close on a session end.
func (s *TVSubscriber) Messages() <-chan TVViewerMessage {
	return s.sub.ch
}

// Close detaches the viewer. It is safe to call after the stream has already
// closed (session end or a slow-drop), and it GCs an idle channel's room.
func (s *TVSubscriber) Close() {
	s.room.mu.Lock()
	s.room.removeSub(s.sub)
	s.room.mu.Unlock()
	s.hub.gcRoom(s.channelID)
}
