package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// SegmentStream is the live transcription port: finalized transcript segments
// for streaming audio. Defined consumer-side so this package does not depend on
// the transcription implementation; the transcribe stream adapter satisfies it.
type SegmentStream interface {
	StreamSegments(ctx context.Context, audio <-chan []byte) (<-chan domain.Segment, error)
}

// LiveEventKind tags a live event as the immediate subtitle for a statement or
// the fact-check result that follows once analysis completes.
type LiveEventKind string

const (
	// LiveEventSubtitle carries a finalized statement's text the moment it is
	// transcribed, before any verdict exists.
	LiveEventSubtitle LiveEventKind = "subtitle"
	// LiveEventResult carries the fact-check outcome for a statement: its
	// matches, or the reason it was not checked, or a non-fatal analysis error.
	LiveEventResult LiveEventKind = "result"
)

// LiveEvent is one incremental output of live analysis. ID is the per-statement
// correlation id shared by a statement's subtitle and its result, so a verdict
// that finalizes after its subtitle reconciles to the right statement. A result
// event mirrors the batch SegmentResult shape (Segment, Matches, SkipReason)
// plus the live-only Err, set when analysis failed without ending the stream.
type LiveEvent struct {
	Kind       LiveEventKind
	ID         string
	Segment    domain.Segment
	Matches    []domain.SegmentMatch
	SkipReason domain.SkipReason
	Err        string
}

// defaultLiveConcurrency bounds in-flight per-segment analyses. Subtitles emit
// in order regardless; this caps how many verdicts compute at once so a burst of
// speech cannot spawn unbounded work, while letting a slow match overlap with
// later statements rather than stalling the live transcript.
const defaultLiveConcurrency = 4

// LiveAnalyzerConfig wires a LiveAnalyzer. Stream and Matcher are required;
// Prechecker defaults to the no-op gate that checks every segment, Logger to
// slog.Default, and Concurrency to defaultLiveConcurrency.
type LiveAnalyzerConfig struct {
	Stream      SegmentStream
	Matcher     SegmentMatcher
	Prechecker  SegmentPrechecker
	Logger      *slog.Logger
	Concurrency int
}

// LiveAnalyzer turns streaming audio into incremental fact-check events. For
// each finalized transcript segment it emits the subtitle immediately, then runs
// the same check-worthiness gate and matcher as the batch pipeline and emits the
// result. It holds no transport types: callers feed it audio bytes and read
// events, and the socket lives entirely in the handler layer.
type LiveAnalyzer struct {
	stream      SegmentStream
	matcher     SegmentMatcher
	prechecker  SegmentPrechecker
	logger      *slog.Logger
	concurrency int
}

// NewLiveAnalyzer builds a LiveAnalyzer from cfg, applying defaults and failing
// when a required collaborator is missing.
func NewLiveAnalyzer(cfg LiveAnalyzerConfig) (*LiveAnalyzer, error) {
	if cfg.Stream == nil {
		return nil, errors.New("service: live analyzer requires a segment stream")
	}
	if cfg.Matcher == nil {
		return nil, errors.New("service: live analyzer requires a matcher")
	}
	prechecker := cfg.Prechecker
	if prechecker == nil {
		prechecker = allowAllPrechecker{}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = defaultLiveConcurrency
	}
	return &LiveAnalyzer{
		stream:      cfg.Stream,
		matcher:     cfg.Matcher,
		prechecker:  prechecker,
		logger:      logger,
		concurrency: concurrency,
	}, nil
}

// Run starts a live analysis over the audio stream and returns the event
// channel. The channel closes when audio closes, the provider ends the stream,
// or ctx is canceled; cancel ctx to stop analysis and release every goroutine.
func (a *LiveAnalyzer) Run(ctx context.Context, audio <-chan []byte) (<-chan LiveEvent, error) {
	segments, err := a.stream.StreamSegments(ctx, audio)
	if err != nil {
		return nil, fmt.Errorf("service: live analyze: %w", err)
	}
	out := make(chan LiveEvent)
	go a.analyzeLoop(ctx, segments, out)
	return out, nil
}

// analyzeLoop emits a subtitle per finalized segment in order and dispatches the
// verdict computation under a concurrency bound, so a slow match overlaps later
// statements instead of stalling the transcript. It closes out only after every
// dispatched worker has finished, so no event is ever sent on a closed channel.
func (a *LiveAnalyzer) analyzeLoop(ctx context.Context, segments <-chan domain.Segment, out chan<- LiveEvent) {
	defer close(out)

	sem := make(chan struct{}, a.concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	seq := 0
	for {
		seg, ok := receiveSegment(ctx, segments)
		if !ok {
			return
		}
		id := strconv.Itoa(seq)
		seq++

		if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventSubtitle, ID: id, Segment: seg}) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(id string, seg domain.Segment) {
			defer wg.Done()
			defer func() { <-sem }()
			sendEvent(ctx, out, a.analyze(ctx, id, seg))
		}(id, seg)
	}
}

// analyze runs the precheck gate, then matches only checkable segments,
// returning the result event. A non-checkable segment carries its skip reason
// and no matches; a precheck or match failure is reported as a non-fatal Err so
// one bad statement never ends the live session. Failures during teardown
// (ctx canceled) are not logged, as the event is dropped on the closing stream.
func (a *LiveAnalyzer) analyze(ctx context.Context, id string, seg domain.Segment) LiveEvent {
	event := LiveEvent{Kind: LiveEventResult, ID: id, Segment: seg}

	decision, err := a.prechecker.Evaluate(ctx, seg.Text)
	if err != nil {
		if ctx.Err() == nil {
			a.logger.ErrorContext(ctx, "live precheck failed", slog.String("id", id), slog.Any("err", err))
		}
		event.Err = "precheck failed"
		return event
	}
	if !decision.Checkable {
		event.SkipReason = decision.Reason
		return event
	}

	matches, err := a.matcher.Match(ctx, seg.Text)
	if err != nil {
		if ctx.Err() == nil {
			a.logger.ErrorContext(ctx, "live match failed", slog.String("id", id), slog.Any("err", err))
		}
		event.Err = "match failed"
		return event
	}
	event.Matches = matches
	return event
}

// receiveSegment reads the next finalized segment, reporting ok=false when the
// stream closes or ctx is canceled.
func receiveSegment(ctx context.Context, segments <-chan domain.Segment) (domain.Segment, bool) {
	select {
	case <-ctx.Done():
		return domain.Segment{}, false
	case seg, ok := <-segments:
		return seg, ok
	}
}

// sendEvent emits one event, reporting false when ctx is canceled before the
// receiver takes it so callers stop instead of blocking on a dead consumer.
func sendEvent(ctx context.Context, out chan<- LiveEvent, event LiveEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- event:
		return true
	}
}
