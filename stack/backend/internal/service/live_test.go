package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeSegmentStream struct {
	segs []domain.Segment
	err  error
}

func (f *fakeSegmentStream) StreamSegments(_ context.Context, _ <-chan []byte) (<-chan domain.Segment, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan domain.Segment, len(f.segs))
	for _, s := range f.segs {
		ch <- s
	}
	close(ch)
	return ch, nil
}

// blockingSegmentStream returns a segment channel that never yields and never
// closes on its own, so the analyzer only stops on ctx cancellation.
type blockingSegmentStream struct{}

func (blockingSegmentStream) StreamSegments(ctx context.Context, _ <-chan []byte) (<-chan domain.Segment, error) {
	ch := make(chan domain.Segment)
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

func TestLiveAnalyzerEmitsSubtitleAndResultPerSegment(t *testing.T) {
	t.Parallel()

	claimMatch := []domain.SegmentMatch{{
		Kind:       domain.MatchKindClaim,
		Claim:      "the earth is an oblate spheroid",
		Verdict:    domain.Verdict("corroborates"),
		Sources:    []domain.Source{},
		Similarity: 0.91,
	}}
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the earth is round"},
		{Start: 2 * time.Second, End: 3 * time.Second, Text: "how are you"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "obscure unmatched claim"},
	}
	analyzer, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream: &fakeSegmentStream{segs: segs},
		Matcher: liveMatcher{
			matches: map[string][]domain.SegmentMatch{"the earth is round": claimMatch},
			err:     map[string]error{"obscure unmatched claim": errors.New("embed failed")},
		},
		Prechecker: livePrechecker{
			skip: map[string]domain.SkipReason{"how are you": domain.SkipReasonNotAClaim},
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
	events := drainLiveEvents(t, out)

	// Subtitles arrive in segment order, one per statement, with sequential ids.
	var subtitles []LiveEvent
	results := make(map[string]LiveEvent)
	for _, ev := range events {
		switch ev.Kind {
		case LiveEventSubtitle:
			subtitles = append(subtitles, ev)
		case LiveEventResult:
			if _, dup := results[ev.ID]; dup {
				t.Fatalf("duplicate result for id %q", ev.ID)
			}
			results[ev.ID] = ev
		}
	}

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
