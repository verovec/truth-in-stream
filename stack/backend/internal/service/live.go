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
	// LiveEventConsistency flags a checkable statement that contradicts an
	// earlier checkable statement by the same speaker. It is additive: it arrives
	// when the stance check completes, after the statement's subtitle and result,
	// and never delays them. Its Consistency field links to the earlier statement.
	LiveEventConsistency LiveEventKind = "consistency"
	// LiveEventClaims carries the atomic claims a checkable unit decomposed into,
	// each with a stable ClaimID the client keys per-claim results on. It is
	// emitted only on the retrieve-then-verify path (FACTCHECK_VERIFY_PATH on),
	// once per unit after its subtitles and before any per-claim result.
	LiveEventClaims LiveEventKind = "claims"
	// LiveEventSpeakerTally carries a speaker's running verdict tallies (credible,
	// disputed, unverifiable, and misleading-framing counts), emitted after each of
	// that speaker's claim verdicts updates the counts (retrieve-then-verify path
	// only). It is additive: a client that ignores it still works, and it is never
	// emitted for an unattributed turn.
	LiveEventSpeakerTally LiveEventKind = "speaker_tally"
)

// LiveEvent is one incremental output of live analysis. ID is the per-statement
// correlation id shared by a statement's subtitle and its result, so a verdict
// that finalizes after its subtitle reconciles to the right statement. A result
// event mirrors the batch SegmentResult shape (Segment, Matches, SkipReason)
// plus the live-only Err, set when analysis failed without ending the stream.
// Confidence is the corroboration score over Matches, set only on a checked
// result and nil for a skipped or errored one.
//
// The remaining fields carry the retrieve-then-verify path's per-claim progress
// and are all zero on the default (flag-off) path, so a flag-off event is
// byte-for-byte the legacy shape. Claims lists a unit's atomic claims on a
// LiveEventClaims event. On a per-claim LiveEventResult, ClaimID keys the claim
// the client replaces in place, ClaimStatus is its lifecycle state, Source tags
// a verified verdict's origin, and Verdict carries the grounded judgment.
type LiveEvent struct {
	Kind        LiveEventKind
	ID          string
	Segment     domain.Segment
	Matches     []domain.SegmentMatch
	SkipReason  domain.SkipReason
	Confidence  *domain.Confidence
	Err         string
	Consistency *ConsistencyFlag
	Claims      []AtomicClaim
	// SegmentIDs lists the unit's member subtitle ids, in order, on a
	// LiveEventClaims event, so a client can render the whole group as one
	// statement. It is additive and empty on every other kind.
	SegmentIDs  []string
	ClaimID     string
	ClaimStatus ClaimStatus
	Source      VerdictSource
	Verdict     *VerifiedVerdict
	// SpeakerTally carries the running per-speaker verdict breakdown on a
	// LiveEventSpeakerTally event; it is nil on every other kind.
	SpeakerTally *SpeakerTally
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
// reads as one tight, coherent claim instead of a paragraph. Four sentences
// give the decomposer enough surrounding speech to extract claims that a
// single sentence leaves ambiguous, while a verdict still reads as one
// thought. The env layer mirrors this default (LIVE_MAX_SENTENCES in
// config.LoadLive) and the two must stay in sync.
const defaultMaxSentences = 4

// defaultIdleFlush bounds how long a buffered unit waits for more same-speaker
// speech before it is scored anyway, so a trailing short turn is checked within
// a couple of seconds of silence rather than held until the next speaker.
const defaultIdleFlush = 2 * time.Second

// defaultMaxHold bounds how long a SILENT unit whose trailing text ends
// mid-sentence is held past the idle window for the rest of the sentence to land.
// Beyond it the fragment is scored anyway, so a sentence that never completes
// still resolves to a verdict within a few seconds instead of hanging. The bound
// counts consecutive idle (silent) windows: fresh same-speaker speech refreshes
// it (continuation is progress toward a complete sentence), so a unit that keeps
// growing is bounded not by MaxHold but by the sentence cap (defaultMaxSentences).
// The effective silent bound is the largest multiple of IdleFlush not exceeding
// MaxHold.
const defaultMaxHold = 6 * time.Second

// defaultConsistencyTopK caps how many of a speaker's topically-closest prior
// statements get a stance call per new statement, so detection cost is bounded
// at a few LLM calls even for a speaker with a long history. defaultConsistency
// Floor is the cosine-similarity floor below which a prior statement is too
// unrelated to be worth a stance call. These are the library defaults applied
// when a caller leaves the field zero; the env layer mirrors them in
// config.LoadConsistency and the two must stay in sync.
const (
	defaultConsistencyTopK  = 3
	defaultConsistencyFloor = 0.6
)

// LiveAnalyzerConfig wires a LiveAnalyzer. Stream and Matcher are required;
// Prechecker defaults to the no-op gate that checks every segment, Logger to
// slog.Default, Concurrency to defaultLiveConcurrency, QueueDepth to
// defaultLiveQueueDepth, MaxSentences to defaultMaxSentences, and IdleFlush to
// defaultIdleFlush, and MaxHold to defaultMaxHold.
// Stance is the optional intra-speaker consistency capability. When nil the
// feature is off and live analysis behaves exactly as before - no consistency
// events, no errors. ConsistencyTopK and ConsistencyFloor tune detection and
// default to defaultConsistencyTopK and defaultConsistencyFloor; they matter
// only when Stance is set.
type LiveAnalyzerConfig struct {
	Stream       SegmentStream
	Matcher      SegmentMatcher
	Prechecker   SegmentPrechecker
	Logger       *slog.Logger
	Concurrency  int
	QueueDepth   int
	MaxSentences int
	IdleFlush    time.Duration
	// MaxHold bounds how long a SILENT unit ending mid-sentence is held past the
	// idle window for the rest of the sentence to land before it is scored anyway.
	// The bound counts consecutive idle windows; fresh same-speaker speech refreshes
	// it, so an actively-growing unit is bounded by MaxSentences, not MaxHold. Zero
	// applies defaultMaxHold; a value below IdleFlush disables the hold, so an
	// incomplete trailing fragment is scored on the first idle tick as before.
	MaxHold          time.Duration
	Stance           StanceClassifier
	ConsistencyTopK  int
	ConsistencyFloor float64
	// Verify, when non-nil, switches a checkable unit onto the retrieve-then-
	// verify path (FACTCHECK_VERIFY_PATH on): decompose into atomic claims, run a
	// fast curated near-match per claim and an evidence-verifier fallback, and
	// emit per-claim progressive results. When nil the analyzer runs the legacy
	// single-pool gate-and-match path unchanged.
	Verify *VerifyPath
}

// LiveAnalyzer turns streaming audio into incremental fact-check events. It
// emits each finalized segment's subtitle immediately, groups consecutive
// same-speaker segments into an analysis unit, then runs the same
// check-worthiness gate and matcher as the batch pipeline on the unit's combined
// text and emits the verdict to each member. It holds no transport types:
// callers feed it audio bytes and read events, and the socket lives entirely in
// the handler layer.
type LiveAnalyzer struct {
	stream           SegmentStream
	matcher          SegmentMatcher
	prechecker       SegmentPrechecker
	logger           *slog.Logger
	concurrency      int
	queueDepth       int
	maxSentences     int
	idleFlush        time.Duration
	maxHold          time.Duration
	stance           StanceClassifier
	consistencyTopK  int
	consistencyFloor float64
	stanceSem        chan struct{}
	verify           *VerifyPath
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
	// A zero MaxHold takes the default; a negative one is clamped to zero, which
	// disables the hold (the loop holds for zero idle windows).
	maxHold := cfg.MaxHold
	if maxHold == 0 {
		maxHold = defaultMaxHold
	}
	if maxHold < 0 {
		maxHold = 0
	}
	consistencyTopK := cfg.ConsistencyTopK
	if consistencyTopK <= 0 {
		consistencyTopK = defaultConsistencyTopK
	}
	consistencyFloor := cfg.ConsistencyFloor
	if consistencyFloor == 0 {
		consistencyFloor = defaultConsistencyFloor
	}
	// The stance semaphore bounds in-flight contradiction calls so a burst of
	// checkable speech cannot fan out unbounded LLM requests; it is sized to the
	// scoring pool and built only when the feature is on.
	var stanceSem chan struct{}
	if cfg.Stance != nil {
		stanceSem = make(chan struct{}, concurrency)
	}
	return &LiveAnalyzer{
		stream:           cfg.Stream,
		matcher:          cfg.Matcher,
		prechecker:       prechecker,
		logger:           logger,
		concurrency:      concurrency,
		queueDepth:       queueDepth,
		maxSentences:     maxSentences,
		idleFlush:        idleFlush,
		maxHold:          maxHold,
		stance:           cfg.Stance,
		consistencyTopK:  consistencyTopK,
		consistencyFloor: consistencyFloor,
		stanceSem:        stanceSem,
		verify:           cfg.Verify,
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
	// Per-speaker memory is created fresh per session and discarded when this
	// loop ends, so a later session never sees a prior one's statements.
	go a.analyzeLoop(ctx, transcripts, out, newSpeakerMemory())
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

// pendingUnit is one analysis unit handed to the worker pool: the members to
// score together, the unit's canonical speaker (the label intra-speaker
// consistency is scoped to), and the trailing sentence of the previously
// flushed unit as decomposition context. Speaker travels alongside the members
// because the unit's normalized speaker is resolved during accumulation and
// would be lost once the members are detached from their liveUnit; context
// travels the same way because only the analyze loop knows what was flushed
// before this unit.
type pendingUnit struct {
	speaker string
	members []unitMember
	context string
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
func (a *LiveAnalyzer) analyzeLoop(ctx context.Context, transcripts <-chan domain.LiveTranscript, out chan<- LiveEvent, mem *speakerMemory) {
	defer close(out)

	// A fixed worker pool drains a bounded backlog of ready units. Sizing the
	// queue separately from the pool lets a burst of fast speech wait for a worker
	// instead of being shed the instant every worker is busy. The deferred close
	// runs before close(out) (LIFO): it stops new work, lets the workers finish
	// the backlog, and only then is out closed.
	queue := make(chan pendingUnit, a.queueDepth)
	var wg sync.WaitGroup
	for range a.concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pu := range queue {
				a.scoreUnit(ctx, out, mem, pu)
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

	// A unit whose trailing text ends mid-sentence is held past the idle window for
	// the rest of the sentence to land, up to maxHold of silence. Hold time is
	// counted in idle windows: idleHolds tracks how many consecutive ones the
	// current unit has been held, reset whenever fresh speech arrives, and
	// maxIdleHolds is the bound. A maxHold below idleFlush yields zero, so the hold
	// is disabled and an incomplete fragment is scored on the first idle tick.
	maxIdleHolds := int(a.maxHold / a.idleFlush)

	var unit liveUnit
	seq := 0
	idleHolds := 0
	// prevContext carries the previously flushed unit's full text (with its
	// speaker, bounded by maxContextWords) into the next unit's decomposition,
	// so a claim opening with a pronoun or an ellipsis still resolves against
	// anything in the group that was just said - across speakers too, since a
	// reply's referent is usually the other speaker's last turn. It is
	// session-local state, discarded with this loop.
	prevContext := ""
	flush := func() bool {
		if unit.empty() {
			return true
		}
		timer.Stop()
		idleHolds = 0
		// Read the speaker into a local before take resets the unit: evaluation
		// order of unit.speaker against unit.take() in one literal is unspecified,
		// so reading it inline can pick up the post-reset empty label.
		speaker := unit.speaker
		members := unit.take()
		pu := pendingUnit{speaker: speaker, members: members, context: prevContext}
		if a.verify != nil {
			// Only the verify path's decomposer consumes the context; the legacy
			// path skips the join-and-extract so its flush cost is unchanged.
			prevContext = contextTail(speaker, combinedText(members))
		}
		return a.dispatch(ctx, out, queue, pu)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// An idle buffer is normally scored now rather than held for speech that
			// may never come. But when its trailing text ends mid-sentence, the unit
			// is held for same-speaker continuation up to maxHold so a verdict is
			// never rendered against a truncated fragment; once held to the bound it
			// is scored anyway so a never-completing sentence still resolves.
			if !unit.empty() && idleHolds < maxIdleHolds && incompleteTrailingFragment(combinedText(unit.members)) {
				idleHolds++
				timer.Reset(a.idleFlush)
				continue
			}
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
			// Fresh same-speaker speech is progress toward a complete sentence, so it
			// refreshes the hold budget; a runaway accumulation is still bounded by
			// the sentence cap below.
			idleHolds = 0
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
func (a *LiveAnalyzer) dispatch(ctx context.Context, out chan<- LiveEvent, queue chan<- pendingUnit, pu pendingUnit) bool {
	select {
	case <-ctx.Done():
		return false
	case queue <- pu:
		return true
	default:
		for _, m := range pu.members {
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
func (a *LiveAnalyzer) scoreUnit(ctx context.Context, out chan<- LiveEvent, mem *speakerMemory, pu pendingUnit) {
	if a.verify != nil {
		a.verify.scoreUnit(ctx, a, out, mem, pu)
		return
	}
	members := pu.members
	text := combinedText(members)
	result, decision, err := a.gateAndMatch(ctx, text)
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
	// The confidence is attached only to a checked statement; a skipped one
	// carries no score. It is a flat read-only value, so every member shares one
	// pointer safely - unlike the matches slice, which each member clones below
	// because a consumer may sort or filter it in place.
	var confidence *domain.Confidence
	if decision.Checkable {
		confidence = &result.Confidence
	}
	for _, m := range members {
		if !sendEvent(ctx, out, LiveEvent{Kind: LiveEventResult, ID: m.id, Segment: m.seg, SkipReason: decision.Reason, Matches: slices.Clone(result.Matches), Confidence: confidence}) {
			return
		}
	}
	// Intra-speaker consistency runs only on a checkable statement, after its
	// verdict is emitted, so it never blocks the subtitle or result. It reuses
	// the matcher's query embedding rather than embedding again.
	if decision.Checkable {
		a.detectConsistency(ctx, out, mem, pu, text, result.QueryEmbedding)
	}
}

// gateAndMatch runs the legacy check-worthiness core for one unit, taking the
// single-embed path when the analyzer's gate and matcher both support it (the
// production wiring: a *Gate with an embedding coverage stage and the segment
// match adapter) and otherwise the two-embed gateAndMatch. Selecting by
// capability keeps a fake gate or matcher in tests, and the no-op allow-all
// prechecker, on the unchanged path - neither embeds twice anyway - while the
// real legacy pipeline collapses its former double embed into one.
func (a *LiveAnalyzer) gateAndMatch(ctx context.Context, text string) (MatchResult, domain.PrecheckDecision, error) {
	if gate, ok := a.prechecker.(*Gate); ok {
		if matcher, ok := a.matcher.(embedOnceMatcher); ok {
			if classifier, coverage, ready := gate.embedOnce(); ready {
				return gateAndMatchEmbedOnce(ctx, classifier, coverage, matcher, text)
			}
		}
	}
	return gateAndMatch(ctx, a.prechecker, a.matcher, text)
}

// detectConsistency flags a checkable statement that contradicts an earlier
// checkable statement by the same speaker, then records the statement for later
// comparisons. It is a no-op unless the feature is configured and the statement
// carries a known speaker and a reusable embedding - an unknown speaker is never
// attributed, so it is neither compared against nor remembered. It cosine-ranks
// the speaker's prior statements, keeps those above the floor, takes the top-k,
// and runs a bounded stance check on each; on the first contradiction it emits
// one consistency event linking the new statement to the earlier one. A stance
// failure is logged and skipped (no flag, no stream termination), so a bad call
// never ends the live session.
func (a *LiveAnalyzer) detectConsistency(ctx context.Context, out chan<- LiveEvent, mem *speakerMemory, pu pendingUnit, text string, embedding []float32) {
	if a.stance == nil || pu.speaker == "" || len(embedding) == 0 || len(pu.members) == 0 {
		return
	}
	// Serialize detection for this speaker so the prior-lookup and the append
	// below bracket the stance check atomically: with the default worker pool,
	// two of the same speaker's units can be scored at once, and without this
	// lock both would read an empty history and miss the contradiction between
	// them. Other speakers hold different locks and run concurrently.
	lock := mem.speakerLock(pu.speaker)
	lock.Lock()
	defer lock.Unlock()

	// The unit's opening subtitle is the statement's canonical id and segment:
	// the combined text was scored as one statement, so its first member anchors
	// the flag the UI renders.
	newID := pu.members[0].id
	newSeg := pu.members[0].seg

	for _, prior := range rankBySimilarity(embedding, mem.prior(pu.speaker), a.consistencyFloor, a.consistencyTopK) {
		contradicts, rationale, err := a.classifyStance(ctx, prior.text, text)
		if err != nil {
			if ctx.Err() == nil {
				a.logger.WarnContext(ctx, "stance check failed", slog.String("id", newID), slog.Any("err", err))
			}
			continue
		}
		if contradicts {
			if !sendEvent(ctx, out, LiveEvent{
				Kind:    LiveEventConsistency,
				ID:      newID,
				Segment: newSeg,
				Consistency: &ConsistencyFlag{
					EarlierID:   prior.id,
					EarlierText: prior.text,
					Speaker:     pu.speaker,
					Rationale:   rationale,
				},
			}) {
				return
			}
			break
		}
	}
	// Remember after comparing so a statement is never compared against itself.
	mem.remember(pu.speaker, priorStatement{id: newID, text: text, embedding: embedding})
}

// detectClaimConsistency is the per-atomic-claim variant of detectConsistency
// used by the retrieve-then-verify path: it compares one atomic claim against
// the speaker's prior atomic claims (reusing the claim's retrieval embedding,
// never re-embedding) and flags the first contradiction, then remembers the
// claim. It is a no-op unless the feature is configured and the claim carries a
// known speaker and a reusable embedding. The consistency event keys on the
// claim's id and segment so the UI links it to the same claim its verdict
// rendered on. A stance failure is logged and skipped, never ending the session.
func (a *LiveAnalyzer) detectClaimConsistency(ctx context.Context, out chan<- LiveEvent, mem *speakerMemory, speaker string, claim AtomicClaim, embedding []float32, seg domain.Segment) {
	if a.stance == nil || speaker == "" || len(embedding) == 0 {
		return
	}
	lock := mem.speakerLock(speaker)
	lock.Lock()
	defer lock.Unlock()

	for _, prior := range rankBySimilarity(embedding, mem.prior(speaker), a.consistencyFloor, a.consistencyTopK) {
		contradicts, rationale, err := a.classifyStance(ctx, prior.text, claim.Text)
		if err != nil {
			if ctx.Err() == nil {
				a.logger.WarnContext(ctx, "stance check failed", slog.String("claim_id", claim.ClaimID), slog.Any("err", err))
			}
			continue
		}
		if contradicts {
			if !sendEvent(ctx, out, LiveEvent{
				Kind:    LiveEventConsistency,
				ID:      claim.ClaimID,
				Segment: seg,
				Consistency: &ConsistencyFlag{
					EarlierID:   prior.id,
					EarlierText: prior.text,
					Speaker:     speaker,
					Rationale:   rationale,
				},
			}) {
				return
			}
			break
		}
	}
	mem.remember(speaker, priorStatement{id: claim.ClaimID, text: claim.Text, embedding: embedding})
}

// classifyStance runs one stance check under the in-flight bound, so a burst of
// checkable speech cannot fan out unbounded LLM calls. It reports ctx
// cancellation as an error so the caller stops rather than treating teardown as
// "no contradiction".
func (a *LiveAnalyzer) classifyStance(ctx context.Context, earlier, later string) (bool, string, error) {
	select {
	case a.stanceSem <- struct{}{}:
		defer func() { <-a.stanceSem }()
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
	return a.stance.Contradicts(ctx, earlier, later)
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

// isTerminator reports whether r ends a sentence. An ellipsis rune counts, as do
// the three ASCII sentence terminators; a run of them ("?!" or "...") is one
// boundary, handled by the callers that collapse consecutive terminators.
func isTerminator(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '…'
}

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
		term := isTerminator(r)
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

// incompleteTrailingFragment reports whether text ends mid-sentence: the trailing
// run since the last sentence terminator (the whole text when it has none) is a
// non-empty open fragment shorter than one sentence's worth of words
// (maxWordsPerSentence). Such a unit is held on the idle path for same-speaker
// continuation rather than scored as a truncated claim. Text ending in terminal
// punctuation leaves no open run (a terminator resets the count), and an open run
// already a full sentence long is complete enough to score now; both, as well as
// empty or whitespace-only text, return false.
func incompleteTrailingFragment(text string) bool {
	trailingWords := 0
	inWord := false
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			inWord = false
		case isTerminator(r):
			// A terminator closes the current sentence: the open run restarts after
			// it, so words counted so far no longer belong to a trailing fragment.
			inWord = false
			trailingWords = 0
		default:
			if !inWord {
				inWord = true
				trailingWords++
			}
		}
	}
	return trailingWords > 0 && trailingWords < maxWordsPerSentence
}

// maxContextWords bounds the decomposition context handed to the next unit: a
// full unit of defaultMaxSentences sentences fits comfortably, while an
// unpunctuated run can never grow the prompt without limit. The most recent
// words are kept, since a reference usually points at what was said last.
const maxContextWords = 120

// contextTail renders one flushed unit's full text as the next unit's
// decomposition context, prefixed with the speaker label when known so a
// cross-speaker reply resolves "he/that" against the right person. The whole
// previous group rides along - not just its last sentence - so a reference to
// anything said in it still resolves; the tail is bounded at maxContextWords
// (keeping the most recent words).
func contextTail(speaker, text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if words := strings.Fields(trimmed); len(words) > maxContextWords {
		trimmed = strings.Join(words[len(words)-maxContextWords:], " ")
	}
	if speaker == "" {
		return trimmed
	}
	return speaker + ": " + trimmed
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
