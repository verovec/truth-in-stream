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

// SegmentStream is the live transcription port: transcript revisions for
// streaming audio, each tagged interim (partial) or final. Defined consumer-side
// so this package does not depend on the transcription implementation; the
// transcribe stream adapter satisfies it.
type SegmentStream interface {
	StreamSegments(ctx context.Context, audio <-chan []byte) (<-chan domain.LiveTranscript, error)
}

// LiveEventKind tags a live event as the immediate subtitle for a statement or
// the fact-check result that follows once analysis completes.
type LiveEventKind string

const (
	// LiveEventInterim carries the live, still-being-revised caption for the
	// current utterance, before the provider commits it. It has no id and is
	// never fact-checked - it exists only so the transcript is visible word by
	// word rather than appearing in bursts when a statement finalizes.
	LiveEventInterim LiveEventKind = "interim"
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
// speech cannot spawn unbounded work. Speech beyond the bound is surfaced as a
// subtitle and reported not_checked rather than stalling the live transcript.
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
	transcripts, err := a.stream.StreamSegments(ctx, audio)
	if err != nil {
		return nil, fmt.Errorf("service: live analyze: %w", err)
	}
	out := make(chan LiveEvent)
	go a.analyzeLoop(ctx, transcripts, out)
	return out, nil
}

// analyzeLoop turns transcript revisions into live events. An interim (partial)
// transcript is forwarded as an interim caption with no id and no scoring, so
// the transcript is visible word by word. A finalized transcript emits a
// subtitle and dispatches the verdict under a concurrency bound. Subtitle
// emission never waits on a worker slot: when every worker is busy the statement
// is reported not_checked at once, so a slow match overlaps later statements
// instead of stalling the transcript. It closes out only after every dispatched
// worker has finished, so no event is ever sent on a closed channel.
func (a *LiveAnalyzer) analyzeLoop(ctx context.Context, transcripts <-chan domain.LiveTranscript, out chan<- LiveEvent) {
	defer close(out)

	sem := make(chan struct{}, a.concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()

	seq := 0
	for {
		tr, ok := receiveTranscript(ctx, transcripts)
		if !ok {
			return
		}
		// Interim revisions are the live caption: surfaced as-is, never scored.
		if !tr.Final {
			if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventInterim, Segment: tr.Segment}) {
				return
			}
			continue
		}
		seg := tr.Segment
		id := strconv.Itoa(seq)
		seq++

		if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventSubtitle, ID: id, Segment: seg}) {
			return
		}
		// Score only when a worker slot is free, so a slow match never stalls the
		// subtitle stream. When every worker is busy the statement keeps its
		// subtitle and is reported unscored rather than holding back later speech.
		// The skip is best-effort and final: the statement is not re-queued, so
		// sustained saturation simply leaves more statements unscored.
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
			wg.Add(1)
			go func(id string, seg domain.Segment) {
				defer wg.Done()
				defer func() { <-sem }()
				sendEvent(ctx, out, a.analyze(ctx, id, seg))
			}(id, seg)
		default:
			if !sendEvent(ctx, out, LiveEvent{
				Kind:       LiveEventResult,
				ID:         id,
				Segment:    seg,
				SkipReason: domain.SkipReasonNotChecked,
			}) {
				return
			}
		}
	}
}

// analyze runs the shared gate-and-match core and shapes the result event. A
// non-checkable segment carries its skip reason and no matches; a precheck or
// match failure is reported as a non-fatal Err so one bad statement never ends
// the live session. Failures during teardown (ctx canceled) are not logged, as
// the event is dropped on the closing stream.
func (a *LiveAnalyzer) analyze(ctx context.Context, id string, seg domain.Segment) LiveEvent {
	event := LiveEvent{Kind: LiveEventResult, ID: id, Segment: seg}

	matches, decision, err := gateAndMatch(ctx, a.prechecker, a.matcher, seg.Text)
	if err != nil {
		if ctx.Err() == nil {
			a.logger.ErrorContext(ctx, "live analysis failed", slog.String("id", id), slog.Any("err", err))
		}
		event.Err = "analysis failed"
		return event
	}
	event.SkipReason = decision.Reason
	event.Matches = matches
	return event
}

// receiveTranscript reads the next transcript revision, reporting ok=false when
// the stream closes or ctx is canceled.
func receiveTranscript(ctx context.Context, transcripts <-chan domain.LiveTranscript) (domain.LiveTranscript, bool) {
	select {
	case <-ctx.Done():
		return domain.LiveTranscript{}, false
	case tr, ok := <-transcripts:
		return tr, ok
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
