package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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

// defaultLiveConcurrency bounds in-flight unit analyses. Subtitles emit in order
// regardless; this caps how many verdicts compute at once so a burst of speech
// cannot spawn unbounded work.
const defaultLiveConcurrency = 4

// defaultLiveQueueDepth bounds the backlog of ready units waiting for a worker.
// A short burst that outpaces the workers is buffered here rather than dropped,
// so transient saturation no longer reports statements not_checked; only when
// this backlog is also full does a unit shed to not_checked rather than stall
// the live transcript. This and defaultLiveConcurrency are the library defaults
// applied when a caller leaves the field zero; the env layer mirrors them in
// config.LoadLive and the two must stay in sync.
const defaultLiveQueueDepth = 32

// defaultMaxSentences bounds an analysis unit to a few sentences so a verdict
// reads as one tight, coherent claim instead of a paragraph.
const defaultMaxSentences = 3

// defaultIdleFlush bounds how long a buffered unit waits for more same-speaker
// speech before it is scored anyway, so a trailing short turn is checked within
// a couple of seconds of silence rather than held until the next speaker.
const defaultIdleFlush = 2 * time.Second

// LiveAnalyzerConfig wires a LiveAnalyzer. Stream and Matcher are required;
// Prechecker defaults to the no-op gate that checks every segment, Logger to
// slog.Default, Concurrency to defaultLiveConcurrency, QueueDepth to
// defaultLiveQueueDepth, MaxSentences to defaultMaxSentences, and IdleFlush to
// defaultIdleFlush.
type LiveAnalyzerConfig struct {
	Stream       SegmentStream
	Matcher      SegmentMatcher
	Prechecker   SegmentPrechecker
	Logger       *slog.Logger
	Concurrency  int
	QueueDepth   int
	MaxSentences int
	IdleFlush    time.Duration
}

// LiveAnalyzer turns streaming audio into incremental fact-check events. It
// emits each finalized segment's subtitle immediately, groups consecutive
// same-speaker segments into an analysis unit, then runs the same
// check-worthiness gate and matcher as the batch pipeline on the unit's combined
// text and emits the verdict to each member. It holds no transport types:
// callers feed it audio bytes and read events, and the socket lives entirely in
// the handler layer.
type LiveAnalyzer struct {
	stream       SegmentStream
	matcher      SegmentMatcher
	prechecker   SegmentPrechecker
	logger       *slog.Logger
	concurrency  int
	queueDepth   int
	maxSentences int
	idleFlush    time.Duration
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
	queueDepth := cfg.QueueDepth
	if queueDepth <= 0 {
		queueDepth = defaultLiveQueueDepth
	}
	maxSentences := cfg.MaxSentences
	if maxSentences <= 0 {
		maxSentences = defaultMaxSentences
	}
	idleFlush := cfg.IdleFlush
	if idleFlush <= 0 {
		idleFlush = defaultIdleFlush
	}
	return &LiveAnalyzer{
		stream:       cfg.Stream,
		matcher:      cfg.Matcher,
		prechecker:   prechecker,
		logger:       logger,
		concurrency:  concurrency,
		queueDepth:   queueDepth,
		maxSentences: maxSentences,
		idleFlush:    idleFlush,
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

// unitMember is one committed segment buffered into the current analysis unit:
// its subtitle correlation id paired with its segment. The unit groups members,
// scores their combined text once, then emits a result per member id so every
// subtitle still resolves to a verdict (the verdict per member subtitle model).
type unitMember struct {
	id  string
	seg domain.Segment
}

// liveUnit accumulates consecutive same-speaker committed segments into one
// analysis unit. speaker is the unit's speaker, adopted from the first member
// that carries a known label; sentences is the running estimate used to cap a
// unit at maxSentences.
type liveUnit struct {
	members   []unitMember
	speaker   string
	sentences int
}

func (u *liveUnit) empty() bool { return len(u.members) == 0 }

// add appends a segment, adopting its speaker label when the unit has none yet
// (so a unit that opened on an unknown-speaker turn takes the next known label).
func (u *liveUnit) add(id string, seg domain.Segment, sentences int) {
	if u.speaker == "" && seg.Speaker != "" {
		u.speaker = seg.Speaker
	}
	u.members = append(u.members, unitMember{id: id, seg: seg})
	u.sentences += sentences
}

// take returns the buffered members and resets the unit for the next one.
func (u *liveUnit) take() []unitMember {
	members := u.members
	*u = liveUnit{}
	return members
}

// combinedText joins the members' text into the single statement that is scored,
// so the verdict reflects the whole same-speaker thought rather than a fragment.
func combinedText(members []unitMember) string {
	var b strings.Builder
	for i, m := range members {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(m.seg.Text)
	}
	return b.String()
}

// analyzeLoop turns transcript revisions into live events. An interim (partial)
// transcript is forwarded as an interim caption with no id and no scoring, so
// the transcript is visible word by word. A finalized transcript emits its
// subtitle immediately (per committed segment, with its speaker, decoupled from
// scoring) and is buffered into the current analysis unit. The unit flushes - is
// scored once and a verdict emitted per member - when the speaker changes, the
// unit reaches the sentence cap, or it idles, so every verdict stays within one
// speaker and reads as a tight claim. A fixed pool of workers drains the backlog
// queue; the loop closes that queue and waits for the pool before it closes out,
// so no event is ever sent on a closed channel.
func (a *LiveAnalyzer) analyzeLoop(ctx context.Context, transcripts <-chan domain.LiveTranscript, out chan<- LiveEvent) {
	defer close(out)

	// A fixed worker pool drains a bounded backlog of ready units. Sizing the
	// queue separately from the pool lets a burst of fast speech wait for a worker
	// instead of being shed the instant every worker is busy. The deferred close
	// runs before close(out) (LIFO): it stops new work, lets the workers finish
	// the backlog, and only then is out closed.
	queue := make(chan []unitMember, a.queueDepth)
	var wg sync.WaitGroup
	for range a.concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for members := range queue {
				a.scoreUnit(ctx, out, members)
			}
		}()
	}
	defer func() {
		close(queue)
		wg.Wait()
	}()

	// The idle timer starts stopped; it is armed whenever a segment is buffered
	// and disarmed on every flush, so it only fires on a genuinely idle buffer.
	// Go 1.23+ guarantees no stale tick is delivered after Stop, so a bare Stop
	// before each Reset is sufficient - no manual channel drain.
	timer := time.NewTimer(a.idleFlush)
	timer.Stop()
	defer timer.Stop()

	var unit liveUnit
	seq := 0
	flush := func() bool {
		if unit.empty() {
			return true
		}
		timer.Stop()
		return a.dispatch(ctx, out, queue, unit.take())
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// An idle buffer is scored now rather than held for speech that may
			// never come, so a trailing short turn still gets a verdict.
			if !flush() {
				return
			}
		case tr, ok := <-transcripts:
			if !ok {
				// Clean end of stream: score the trailing buffered unit so the last
				// finalized statements still get a verdict rather than being lost.
				// On ctx cancel the case above returns first and skips this flush.
				flush()
				return
			}
			if !tr.Final {
				if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventInterim, Segment: tr.Segment}) {
					return
				}
				continue
			}
			seg := tr.Segment
			id := strconv.Itoa(seq)
			seq++
			// The subtitle is the live caption: emitted at once, never waiting on
			// scoring, so the transcript stays responsive regardless of grouping.
			if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventSubtitle, ID: id, Segment: seg}) {
				return
			}
			sentences := sentenceCount(seg.Text)
			// A speaker change or an over-cap append closes the current unit before
			// this segment joins, so a unit never blends speakers or runs long.
			if !unit.empty() && (!sameSpeaker(seg.Speaker, unit.speaker) || unit.sentences+sentences > a.maxSentences) {
				if !flush() {
					return
				}
			}
			unit.add(id, seg, sentences)
			timer.Stop()
			timer.Reset(a.idleFlush)
			// A full unit is scored at once instead of waiting for a boundary, so
			// its verdict is not delayed behind the idle window.
			if unit.sentences >= a.maxSentences {
				if !flush() {
					return
				}
			}
		}
	}
}

// dispatch hands one analysis unit to the worker pool through the bounded queue.
// The enqueue is non-blocking, so the transcript reader never stalls behind
// scoring: while a worker or queue slot is free the unit is buffered and scored
// in order; only when the queue is full are the unit's members reported
// not_checked at once, so a statement is shed only under genuine sustained
// saturation rather than on the first busy worker. It reports false when ctx is
// canceled mid-emit.
func (a *LiveAnalyzer) dispatch(ctx context.Context, out chan<- LiveEvent, queue chan<- []unitMember, members []unitMember) bool {
	select {
	case <-ctx.Done():
		return false
	case queue <- members:
		return true
	default:
		for _, m := range members {
			if !sendEvent(ctx, out, LiveEvent{
				Kind:       LiveEventResult,
				ID:         m.id,
				Segment:    m.seg,
				SkipReason: domain.SkipReasonNotChecked,
			}) {
				return false
			}
		}
		return true
	}
}

// scoreUnit runs the shared gate-and-match core on the unit's combined text and
// emits the same verdict to each member, keyed to that member's subtitle id and
// segment so every subtitle reconciles to a result. A non-checkable unit carries
// its skip reason and no matches; a precheck or match failure is reported as a
// non-fatal Err on each member so one bad unit never ends the live session.
// Failures during teardown (ctx canceled) are not logged, as the event is
// dropped on the closing stream.
func (a *LiveAnalyzer) scoreUnit(ctx context.Context, out chan<- LiveEvent, members []unitMember) {
	matches, decision, err := gateAndMatch(ctx, a.prechecker, a.matcher, combinedText(members))
	if err != nil {
		if ctx.Err() == nil {
			a.logger.ErrorContext(ctx, "live analysis failed", slog.String("ids", memberIDs(members)), slog.Any("err", err))
		}
		for _, m := range members {
			if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: m.id, Segment: m.seg, Err: "analysis failed"}) {
				return
			}
		}
		return
	}
	for _, m := range members {
		// Each member gets its own copy of the verdict's matches so the per-member
		// events stay independent: a consumer that mutates one result's matches
		// (sort, filter) cannot corrupt its siblings through a shared backing array.
		if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: m.id, Segment: m.seg, SkipReason: decision.Reason, Matches: slices.Clone(matches)}) {
			return
		}
	}
}

// memberIDs joins the members' ids for one log line attributing a failed unit.
func memberIDs(members []unitMember) string {
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.id
	}
	return strings.Join(ids, ",")
}

// sameSpeaker reports whether an incoming segment continues the unit's speaker.
// An empty (unknown) label on either side is treated as a continuation rather
// than a new speaker, so a transient unknown label from a very short turn does
// not split a unit spuriously.
func sameSpeaker(incoming, current string) bool {
	return incoming == "" || current == "" || incoming == current
}

// maxWordsPerSentence is the fallback sentence length: a same-speaker run with no
// terminal punctuation still counts as multiple sentences past this many words,
// so an unpunctuated monologue flushes instead of growing without bound.
const maxWordsPerSentence = 25

// sentenceCount estimates how many sentences a segment holds in a single
// allocation-free pass. It counts groups of terminal punctuation (so "?!" or an
// ellipsis is one boundary, not several) and, as a fallback for unpunctuated
// speech, the word count divided by maxWordsPerSentence, taking the larger so
// both well-punctuated speech and a long unpunctuated run are bounded. Empty or
// whitespace-only text holds no sentences.
func sentenceCount(text string) int {
	terminators, words := 0, 0
	prevTerm, inWord := false, false
	for _, r := range text {
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			inWord = true
			words++
		}
		term := r == '.' || r == '!' || r == '?' || r == '…'
		if term && !prevTerm {
			terminators++
		}
		prevTerm = term
	}
	if words == 0 {
		return 0
	}
	byWords := (words + maxWordsPerSentence - 1) / maxWordsPerSentence
	return max(1, max(terminators, byWords))
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
