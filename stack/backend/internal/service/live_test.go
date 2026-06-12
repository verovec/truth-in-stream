package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeSegmentStream struct {
	transcripts []domain.LiveTranscript
	err         error
}

func (f *fakeSegmentStream) StreamSegments(_ context.Context, _ <-chan []byte) (<-chan domain.LiveTranscript, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan domain.LiveTranscript, len(f.transcripts))
	for _, t := range f.transcripts {
		ch <- t
	}
	close(ch)
	return ch, nil
}

// finalize wraps segments as finalized transcripts, the input the scoring path
// expects; tests that exercise interim captions add partials explicitly.
func finalize(segs ...domain.Segment) []domain.LiveTranscript {
	out := make([]domain.LiveTranscript, len(segs))
	for i, s := range segs {
		out[i] = domain.LiveTranscript{Segment: s, Final: true}
	}
	return out
}

// pausingStream replays its transcripts then holds the channel open until ctx is
// canceled, so a test can observe the idle-flush timer fire on a buffered unit
// that no further speech follows.
type pausingStream struct {
	transcripts []domain.LiveTranscript
}

func (p pausingStream) StreamSegments(ctx context.Context, _ <-chan []byte) (<-chan domain.LiveTranscript, error) {
	ch := make(chan domain.LiveTranscript)
	go func() {
		defer close(ch)
		for _, t := range p.transcripts {
			select {
			case ch <- t:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return ch, nil
}

// collectByKind splits drained events into the per-kind groups the grouping
// tests assert over: subtitles in order, results keyed by correlation id.
func collectByKind(events []LiveEvent) (subtitles []LiveEvent, results map[string]LiveEvent) {
	results = make(map[string]LiveEvent)
	for _, ev := range events {
		switch ev.Kind {
		case LiveEventSubtitle:
			subtitles = append(subtitles, ev)
		case LiveEventResult:
			results[ev.ID] = ev
		}
	}
	return subtitles, results
}

// blockingSegmentStream returns a transcript channel that never yields and never
// closes on its own, so the analyzer only stops on ctx cancellation.
type blockingSegmentStream struct{}

func (blockingSegmentStream) StreamSegments(ctx context.Context, _ <-chan []byte) (<-chan domain.LiveTranscript, error) {
	ch := make(chan domain.LiveTranscript)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

type livePrechecker struct {
	skip map[string]domain.SkipReason
	err  map[string]error
}

func (f livePrechecker) Evaluate(_ context.Context, text string) (domain.PrecheckDecision, error) {
	if err := f.err[text]; err != nil {
		return domain.PrecheckDecision{}, err
	}
	if reason, ok := f.skip[text]; ok {
		return domain.Skipped(reason), nil
	}
	return domain.Checkable(), nil
}

type liveMatcher struct {
	matches map[string][]domain.SegmentMatch
	err     map[string]error
}

func (f liveMatcher) Match(_ context.Context, text string) ([]domain.SegmentMatch, error) {
	if err := f.err[text]; err != nil {
		return nil, err
	}
	return f.matches[text], nil
}

// countingMatcher records the text of every Match call in order, so a test can
// assert how the aggregator grouped committed segments into analysis units.
type countingMatcher struct {
	matches map[string][]domain.SegmentMatch

	mu   sync.Mutex
	seen []string
}

func (m *countingMatcher) Match(_ context.Context, text string) ([]domain.SegmentMatch, error) {
	m.mu.Lock()
	m.seen = append(m.seen, text)
	m.mu.Unlock()
	return m.matches[text], nil
}

func (m *countingMatcher) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.seen...)
}

// sortedCalls returns the scored texts sorted, so a test asserts which units
// were scored without depending on the order concurrent workers ran in.
func sortedCalls(m *countingMatcher) []string {
	calls := m.calls()
	slices.Sort(calls)
	return calls
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

// blockingMatcher holds every Match call until release is closed, so a test can
// pin the verdict workers as busy and observe how the analyzer behaves while
// scoring is saturated.
type blockingMatcher struct {
	release <-chan struct{}
}

func (m blockingMatcher) Match(ctx context.Context, _ string) ([]domain.SegmentMatch, error) {
	select {
	case <-m.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func drainLiveEvents(t *testing.T, out <-chan LiveEvent) []LiveEvent {
	t.Helper()
	var events []LiveEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range out {
			events = append(events, ev)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live event channel to close")
	}
	return events
}

func TestLiveAnalyzerScoresEachSpeakerTurnAsItsOwnUnit(t *testing.T) {
	t.Parallel()
	// Distinct speakers on consecutive turns each close the prior unit, so every
	// turn is scored alone: a per-speaker unit of one segment behaves like the
	// pre-grouping per-segment path, one subtitle and one result per statement.
	claimMatch := []domain.SegmentMatch{{
		Kind:       domain.MatchKindClaim,
		Claim:      "the earth is an oblate spheroid",
		Verdict:    domain.Verdict("corroborates"),
		Sources:    []domain.Source{},
		Similarity: 0.91,
	}}
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the earth is round.", Speaker: "A"},
		{Start: 2 * time.Second, End: 3 * time.Second, Text: "how are you?", Speaker: "B"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "obscure unmatched claim.", Speaker: "C"},
	}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream: &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher: liveMatcher{
			matches: map[string][]domain.SegmentMatch{"the earth is round.": claimMatch},
			err:     map[string]error{"obscure unmatched claim.": errors.New("embed failed")},
		},
		Prechecker: livePrechecker{
			skip: map[string]domain.SkipReason{"how are you?": domain.SkipReasonNotAClaim},
		},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	subtitles, results := collectByKind(drainLiveEvents(t, out))

	// Subtitles arrive per committed segment, in order, with their speaker.
	wantSubtitles := []LiveEvent{
		{Kind: LiveEventSubtitle, ID: "0", Segment: segs[0]},
		{Kind: LiveEventSubtitle, ID: "1", Segment: segs[1]},
		{Kind: LiveEventSubtitle, ID: "2", Segment: segs[2]},
	}
	if diff := cmp.Diff(wantSubtitles, subtitles); diff != "" {
		t.Errorf("subtitles mismatch (-want +got):\n%s", diff)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 result events, got %d", len(results))
	}
	// id 0: checkable, carries the claim match.
	if diff := cmp.Diff(claimMatch, results["0"].Matches); diff != "" {
		t.Errorf("result 0 matches mismatch (-want +got):\n%s", diff)
	}
	if results["0"].SkipReason != domain.SkipReasonNone || results["0"].Err != "" {
		t.Errorf("result 0 should be a clean checked verdict, got %+v", results["0"])
	}
	// id 1: skipped as not a claim, never matched.
	if results["1"].SkipReason != domain.SkipReasonNotAClaim || len(results["1"].Matches) != 0 {
		t.Errorf("result 1 should be skipped not_a_claim, got %+v", results["1"])
	}
	// id 2: checkable but matching failed; reported as a non-fatal error.
	if results["2"].Err != "analysis failed" || len(results["2"].Matches) != 0 {
		t.Errorf("result 2 should carry an analysis error, got %+v", results["2"])
	}
}

func TestLiveAnalyzerGroupsSameSpeakerRunIntoOneUnit(t *testing.T) {
	t.Parallel()
	// Three short same-speaker turns are one analysis unit: scored once on the
	// joined text, with the verdict emitted to every member subtitle id so each
	// statement still resolves to a result.
	claimMatch := []domain.SegmentMatch{{
		Kind:    domain.MatchKindClaim,
		Claim:   "grouped claim",
		Verdict: domain.Verdict("contradicts"),
		Sources: []domain.Source{},
	}}
	segs := []domain.Segment{
		{Start: 1 * time.Second, End: 2 * time.Second, Text: "the moon is flat.", Speaker: "A"},
		{Start: 2 * time.Second, End: 3 * time.Second, Text: "i am certain.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "trust me.", Speaker: "A"},
	}
	mc := &countingMatcher{matches: map[string][]domain.SegmentMatch{
		"the moon is flat. i am certain. trust me.": claimMatch,
	}}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:    mc,
		Prechecker: livePrechecker{},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	subtitles, results := collectByKind(drainLiveEvents(t, out))

	if len(subtitles) != 3 {
		t.Fatalf("expected 3 subtitles, got %d", len(subtitles))
	}
	// The unit is scored exactly once, on the joined text.
	if got := mc.calls(); len(got) != 1 || got[0] != "the moon is flat. i am certain. trust me." {
		t.Fatalf("matcher should be called once on the joined text, got %v", got)
	}
	if len(results) != 3 {
		t.Fatalf("expected a result per member, got %d", len(results))
	}
	for _, id := range []string{"0", "1", "2"} {
		if diff := cmp.Diff(claimMatch, results[id].Matches); diff != "" {
			t.Errorf("result %s should carry the unit verdict (-want +got):\n%s", id, diff)
		}
		if results[id].Segment.Text != segs[mustAtoi(id)].Text {
			t.Errorf("result %s text = %q, want its own segment text %q", id, results[id].Segment.Text, segs[mustAtoi(id)].Text)
		}
	}
}

func TestLiveAnalyzerSplitsSameSpeakerRunAtSentenceCap(t *testing.T) {
	t.Parallel()
	// A same-speaker run longer than the cap splits into successive units of at
	// most three sentences: the first three turns score together, the fourth
	// opens a new unit.
	segs := []domain.Segment{
		{Text: "one.", Speaker: "A"},
		{Text: "two.", Speaker: "A"},
		{Text: "three.", Speaker: "A"},
		{Text: "four.", Speaker: "A"},
	}
	mc := &countingMatcher{}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:    mc,
		Prechecker: livePrechecker{},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, results := collectByKind(drainLiveEvents(t, out))

	// Units score on concurrent workers, so compare the set of scored texts, not
	// their order.
	wantCalls := []string{"four.", "one. two. three."}
	if diff := cmp.Diff(wantCalls, sortedCalls(mc)); diff != "" {
		t.Errorf("unit boundaries mismatch (-want +got):\n%s", diff)
	}
	if len(results) != 4 {
		t.Errorf("every member must still get a result, got %d", len(results))
	}
}

func TestLiveAnalyzerFlushesPriorSpeakerBeforeNewSpeaker(t *testing.T) {
	t.Parallel()
	// Speaker A's buffered run is scored as one unit the moment speaker B's turn
	// arrives, so A's words never blend into B's verdict.
	segs := []domain.Segment{
		{Text: "alpha one.", Speaker: "A"},
		{Text: "alpha two.", Speaker: "A"},
		{Text: "bravo one.", Speaker: "B"},
	}
	mc := &countingMatcher{}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:    mc,
		Prechecker: livePrechecker{},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	drainLiveEvents(t, out)

	wantCalls := []string{"alpha one. alpha two.", "bravo one."}
	if diff := cmp.Diff(wantCalls, sortedCalls(mc)); diff != "" {
		t.Errorf("unit boundaries mismatch (-want +got):\n%s", diff)
	}
}

func TestLiveAnalyzerTreatsUnknownSpeakerAsContinuation(t *testing.T) {
	t.Parallel()
	// A turn whose diarization label is empty (unknown) joins the current
	// speaker's unit rather than splitting it, so a transient unknown label does
	// not fragment one thought.
	segs := []domain.Segment{
		{Text: "first.", Speaker: "A"},
		{Text: "second.", Speaker: ""},
	}
	mc := &countingMatcher{}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:    mc,
		Prechecker: livePrechecker{},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	drainLiveEvents(t, out)

	wantCalls := []string{"first. second."}
	if diff := cmp.Diff(wantCalls, mc.calls()); diff != "" {
		t.Errorf("unknown speaker should continue the unit (-want +got):\n%s", diff)
	}
}

func TestLiveAnalyzerIdleFlushesTrailingShortTurn(t *testing.T) {
	// A trailing short turn with no further speech must still be scored once the
	// idle window elapses, rather than held forever. synctest drives the idle
	// timer on a fake clock so the test is deterministic and sleeps nowhere.
	synctest.Test(t, func(t *testing.T) {
		match := []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Claim: "lone", Sources: []domain.Source{}}}
		seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "a lone remark.", Speaker: "A"}
		analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
			Stream:     pausingStream{transcripts: finalize(seg)},
			Matcher:    liveMatcher{matches: map[string][]domain.SegmentMatch{"a lone remark.": match}},
			Prechecker: livePrechecker{},
			Logger:     discardLogger(),
			IdleFlush:  2 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewLiveAnalyzer: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		out, err := analyzer.Run(ctx, make(chan []byte))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// The subtitle is emitted at once. The result then arrives only after the
		// idle window: with every other goroutine blocked, reading it advances the
		// fake clock until the idle timer fires and scores the buffered turn.
		if sub := <-out; sub.Kind != LiveEventSubtitle {
			t.Fatalf("first event = %v, want a subtitle", sub.Kind)
		}
		res := <-out
		if res.Kind != LiveEventResult || res.ID != "0" {
			t.Fatalf("second event = %+v, want the idle-flushed result for id 0", res)
		}
		if diff := cmp.Diff(match, res.Matches); diff != "" {
			t.Errorf("idle-flushed result mismatch (-want +got):\n%s", diff)
		}

		cancel()
		drainLiveEvents(t, out) // let the bubble's goroutines exit before asserting
	})
}

func TestLiveAnalyzerForwardsInterimCaptionsWithoutScoring(t *testing.T) {
	t.Parallel()
	// Partials are surfaced as interim captions - no id, never scored - so the
	// transcript is visible word by word; only the finalized statement gets a
	// subtitle and a verdict.
	transcripts := []domain.LiveTranscript{
		{Segment: domain.Segment{Text: "the ear"}, Final: false},
		{Segment: domain.Segment{Text: "the earth is"}, Final: false},
		{Segment: domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round"}, Final: true},
	}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     &fakeSegmentStream{transcripts: transcripts},
		Matcher:    liveMatcher{},
		Prechecker: livePrechecker{},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	events := drainLiveEvents(t, out)

	var interims []LiveEvent
	var subtitles, results int
	for _, ev := range events {
		switch ev.Kind {
		case LiveEventInterim:
			interims = append(interims, ev)
		case LiveEventSubtitle:
			subtitles++
		case LiveEventResult:
			results++
		}
	}

	wantInterims := []LiveEvent{
		{Kind: LiveEventInterim, Segment: domain.Segment{Text: "the ear"}},
		{Kind: LiveEventInterim, Segment: domain.Segment{Text: "the earth is"}},
	}
	if diff := cmp.Diff(wantInterims, interims); diff != "" {
		t.Errorf("interim captions mismatch (-want +got):\n%s", diff)
	}
	// The finalized statement yields exactly one subtitle and one verdict.
	if subtitles != 1 || results != 1 {
		t.Errorf("finalized statement should yield 1 subtitle + 1 result, got %d + %d", subtitles, results)
	}
}

func TestLiveAnalyzerShedsToNotCheckedOnlyWhenQueueFull(t *testing.T) {
	t.Parallel()
	// Distinct speakers make each turn its own unit. One verdict worker is pinned
	// on the first unit and the backlog queue holds one more, so total capacity is
	// two units; the third exceeds it. The capacity-bound units shed to
	// not_checked, but every statement still surfaces as a subtitle - shedding
	// never stalls the transcript behind the busy worker.
	release := make(chan struct{})
	segs := []domain.Segment{
		{Start: 1 * time.Second, End: 2 * time.Second, Text: "first", Speaker: "A"},
		{Start: 2 * time.Second, End: 3 * time.Second, Text: "second", Speaker: "B"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "third", Speaker: "C"},
	}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:      &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:     blockingMatcher{release: release},
		Prechecker:  livePrechecker{},
		Logger:      discardLogger(),
		Concurrency: 1,
		QueueDepth:  1,
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	subtitles := map[string]bool{}
	notChecked := map[string]bool{}
	released := false
	timeout := time.After(2 * time.Second)
	for done := false; !done; {
		// Worker (1) plus queue (1) absorb two units; the third must shed. Once
		// every subtitle is in and at least one statement has shed, release the
		// pinned worker so the buffered and in-flight units finish and out closes.
		if len(subtitles) == 3 && len(notChecked) >= 1 && !released {
			close(release)
			released = true
		}
		select {
		case ev, ok := <-out:
			if !ok {
				done = true
				break
			}
			switch ev.Kind {
			case LiveEventSubtitle:
				subtitles[ev.ID] = true
			case LiveEventResult:
				if ev.SkipReason == domain.SkipReasonNotChecked {
					notChecked[ev.ID] = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out; subtitles stalled behind saturated scoring (subtitles=%d not_checked=%d)", len(subtitles), len(notChecked))
		}
	}

	if len(subtitles) != 3 {
		t.Errorf("got %d subtitles, want 3 (every statement must surface)", len(subtitles))
	}
	// Two units fit (one in-flight, one queued); the rest shed. At least one
	// statement is over capacity, and the in-flight unit 0 is never shed.
	if len(notChecked) == 0 {
		t.Errorf("expected at least one statement to shed to not_checked, got none")
	}
	if notChecked["0"] {
		t.Errorf("unit 0 held the worker slot and must not shed, got %v", notChecked)
	}
}

func TestLiveAnalyzerBuffersBurstWithinCapacity(t *testing.T) {
	t.Parallel()
	// The behavior change this card delivers: a burst that a single worker cannot
	// score at once is buffered rather than dropped. Three distinct-speaker units
	// arrive while the only worker is pinned; the queue depth exceeds the backlog,
	// so none shed. When the worker is released every unit is scored - no statement
	// is reported not_checked merely because the worker was momentarily busy.
	release := make(chan struct{})
	segs := []domain.Segment{
		{Start: 1 * time.Second, End: 2 * time.Second, Text: "first", Speaker: "A"},
		{Start: 2 * time.Second, End: 3 * time.Second, Text: "second", Speaker: "B"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "third", Speaker: "C"},
	}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:      &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:     blockingMatcher{release: release},
		Prechecker:  livePrechecker{},
		Logger:      discardLogger(),
		Concurrency: 1,
		QueueDepth:  4,
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	subtitles := map[string]bool{}
	results := map[string]LiveEvent{}
	released := false
	timeout := time.After(2 * time.Second)
	for done := false; !done; {
		// Capacity (1 worker + 4 queued) exceeds the 3-unit backlog, so nothing
		// sheds before the worker is freed. Release once every subtitle is in.
		if len(subtitles) == 3 && !released {
			close(release)
			released = true
		}
		select {
		case ev, ok := <-out:
			if !ok {
				done = true
				break
			}
			switch ev.Kind {
			case LiveEventSubtitle:
				subtitles[ev.ID] = true
			case LiveEventResult:
				results[ev.ID] = ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for buffered burst to be scored (subtitles=%d results=%d)", len(subtitles), len(results))
		}
	}

	if len(subtitles) != 3 {
		t.Errorf("got %d subtitles, want 3", len(subtitles))
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want a result per statement", len(results))
	}
	// Every buffered unit was scored once the worker freed up; none was shed.
	for _, id := range []string{"0", "1", "2"} {
		if results[id].SkipReason == domain.SkipReasonNotChecked {
			t.Errorf("statement %s was shed to not_checked despite spare queue capacity", id)
		}
	}
}

func TestLiveAnalyzerEmitsUnitMembersInOrder(t *testing.T) {
	t.Parallel()
	// One same-speaker unit is scored by a single worker, which emits a result per
	// member in member order; the wire must preserve that order so each subtitle's
	// verdict follows in sequence.
	segs := []domain.Segment{
		{Text: "one.", Speaker: "A"},
		{Text: "two.", Speaker: "A"},
		{Text: "three.", Speaker: "A"},
	}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:    &countingMatcher{},
		Prechecker: livePrechecker{},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var resultIDs []string
	for _, ev := range drainLiveEvents(t, out) {
		if ev.Kind == LiveEventResult {
			resultIDs = append(resultIDs, ev.ID)
		}
	}
	if diff := cmp.Diff([]string{"0", "1", "2"}, resultIDs); diff != "" {
		t.Errorf("unit member results out of order (-want +got):\n%s", diff)
	}
}

func TestLiveAnalyzerNoGoroutineLeakOnCancel(t *testing.T) {
	// Cancel mid-flight while a worker is pinned and units sit in the backlog
	// queue, then drain to completion. synctest fails the test if any analyzer
	// goroutine (worker pool or producer) is still running when the bubble's root
	// returns, so a clean exit is asserted by construction.
	synctest.Test(t, func(t *testing.T) {
		segs := []domain.Segment{
			{Text: "first", Speaker: "A"},
			{Text: "second", Speaker: "B"},
			{Text: "third", Speaker: "C"},
		}
		analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
			Stream: pausingStream{transcripts: finalize(segs...)},
			// Never released: the worker stays pinned until ctx cancel unblocks it.
			Matcher:     blockingMatcher{release: make(chan struct{})},
			Prechecker:  livePrechecker{},
			Logger:      discardLogger(),
			Concurrency: 1,
			QueueDepth:  4,
		})
		if err != nil {
			t.Fatalf("NewLiveAnalyzer: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		out, err := analyzer.Run(ctx, make(chan []byte))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// Drain in a goroutine so canceling does not deadlock on an unread event.
		drained := make(chan struct{})
		seen := 0
		go func() {
			defer close(drained)
			for range out {
				seen++
			}
		}()

		// Let the producer enqueue the units and pin the worker, then cancel and
		// confirm everything tears down: the worker's matcher returns on ctx.Done,
		// the producer returns, the queue closes, and out closes.
		synctest.Wait()
		cancel()
		<-drained
		t.Logf("drained %d events after cancel", seen)
	})
}

func TestLiveAnalyzerStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:     blockingSegmentStream{},
		Matcher:    liveMatcher{},
		Prechecker: livePrechecker{},
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	out, err := analyzer.Run(ctx, make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cancel()

	if events := drainLiveEvents(t, out); len(events) != 0 {
		t.Errorf("expected no events after cancel, got %d", len(events))
	}
}

func TestLiveAnalyzerSurfacesStreamSetupError(t *testing.T) {
	t.Parallel()

	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream:  &fakeSegmentStream{err: errors.New("dial failed")},
		Matcher: liveMatcher{},
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err == nil {
		t.Fatal("want stream setup error, got nil")
	}
	if out != nil {
		t.Fatalf("out = %v, want nil", out)
	}
}

func TestNewLiveAnalyzerValidates(t *testing.T) {
	t.Parallel()

	if _, err := NewLiveAnalyzer(LiveAnalyzerConfig{Matcher: liveMatcher{}}); err == nil {
		t.Error("expected error when stream is missing")
	}
	if _, err := NewLiveAnalyzer(LiveAnalyzerConfig{Stream: &fakeSegmentStream{}}); err == nil {
		t.Error("expected error when matcher is missing")
	}
}
