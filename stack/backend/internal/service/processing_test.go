package service

import (
	stdcmp "cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeTranscriber struct {
	mu    sync.Mutex
	calls int
	segs  []domain.Segment
	err   error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, _ string) ([]domain.Segment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.segs, nil
}

func (f *fakeTranscriber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeTranscriber) setSegs(segs []domain.Segment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.segs = segs
}

type fakeMatcher struct {
	mu      sync.Mutex
	matches []domain.SegmentMatch
	errOn   string
	gate    chan struct{}
}

func (f *fakeMatcher) Match(ctx context.Context, text string) ([]domain.SegmentMatch, error) {
	f.mu.Lock()
	gate := f.gate
	errOn := f.errOn
	matches := f.matches
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-gate:
		}
	}
	if errOn != "" && errOn == text {
		return nil, errors.New("matcher exploded")
	}
	return matches, nil
}

func (f *fakeMatcher) setErrOn(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errOn = text
}

// fakePrechecker returns a fixed decision per segment text, defaulting to
// checkable for any text not in the map.
type fakePrechecker struct {
	decisions map[string]domain.PrecheckDecision
	err       error
}

func (f *fakePrechecker) Evaluate(_ context.Context, text string) (domain.PrecheckDecision, error) {
	if f.err != nil {
		return domain.PrecheckDecision{}, f.err
	}
	if d, ok := f.decisions[text]; ok {
		return d, nil
	}
	return domain.Checkable(), nil
}

type memStore struct {
	mu        sync.Mutex
	results   map[string]map[int64]domain.SegmentResult
	processed map[string]int
	saveErr   error
	markErr   error
}

func newMemStore() *memStore {
	return &memStore{
		results:   map[string]map[int64]domain.SegmentResult{},
		processed: map[string]int{},
	}
}

func (m *memStore) SaveSegmentResult(_ context.Context, videoID string, r domain.SegmentResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	if m.results[videoID] == nil {
		m.results[videoID] = map[int64]domain.SegmentResult{}
	}
	m.results[videoID][r.Start.Milliseconds()] = r
	return nil
}

func (m *memStore) DeleteSegmentResults(_ context.Context, videoID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.results, videoID)
	return nil
}

func (m *memStore) MarkVideoProcessed(_ context.Context, videoID string, segmentCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markErr != nil {
		return m.markErr
	}
	m.processed[videoID] = segmentCount
	return nil
}

func (m *memStore) ProcessedSegmentCount(_ context.Context, videoID string) (int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.processed[videoID]
	return n, ok, nil
}

func (m *memStore) ListSegmentResults(_ context.Context, videoID string) ([]domain.SegmentResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.SegmentResult, 0, len(m.results[videoID]))
	for _, r := range m.results[videoID] {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b domain.SegmentResult) int {
		return stdcmp.Compare(a.Start, b.Start)
	})
	return out, nil
}

func (m *memStore) savedCount(videoID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.results[videoID])
}

func (m *memStore) isProcessed(videoID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.processed[videoID]
	return ok
}

func testSegments(n int) []domain.Segment {
	out := make([]domain.Segment, n)
	for i := range n {
		out[i] = domain.Segment{
			Start: time.Duration(i) * time.Second,
			End:   time.Duration(i)*time.Second + 500*time.Millisecond,
			Text:  fmt.Sprintf("segment %d", i),
		}
	}
	return out
}

func testMatches() []domain.SegmentMatch {
	return []domain.SegmentMatch{{
		Kind:       domain.MatchKindClaim,
		Claim:      "the sky is blue",
		Verdict:    domain.VerdictCorroborates,
		Sources:    []domain.Source{{Title: "Sky study", URL: "https://sky.example"}},
		Similarity: 0.92,
	}}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// startRun launches the processor's worker loop for the duration of the test.
func startRun(t *testing.T, p *Processor) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel
}

func TestVideoID(t *testing.T) {
	t.Parallel()
	a := VideoID("https://example.com/v.mp4")
	b := VideoID("https://example.com/v.mp4")
	c := VideoID("https://example.com/other.mp4")
	if a != b {
		t.Errorf("VideoID not stable: %q != %q", a, b)
	}
	if a == c {
		t.Errorf("distinct sources produced the same id %q", a)
	}
	if len(a) != 64 {
		t.Errorf("VideoID length = %d, want 64 hex chars", len(a))
	}
}

func TestSubmitRejectsEmptySource(t *testing.T) {
	t.Parallel()
	p := NewProcessor(ProcessorConfig{
		Transcriber: &fakeTranscriber{},
		Matcher:     &fakeMatcher{},
		Store:       newMemStore(),
		Logger:      testLogger(),
	})
	_, err := p.Submit(t.Context(), "")
	if !errors.Is(err, ErrEmptySource) {
		t.Fatalf("Submit(\"\") err = %v, want ErrEmptySource", err)
	}
}

func TestSubmitQueueFull(t *testing.T) {
	t.Parallel()
	p := NewProcessor(ProcessorConfig{
		Transcriber: &fakeTranscriber{},
		Matcher:     &fakeMatcher{},
		Store:       newMemStore(),
		Logger:      testLogger(),
		QueueSize:   1,
	})
	if _, err := p.Submit(t.Context(), "video-1"); err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	_, err := p.Submit(t.Context(), "video-2")
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second Submit err = %v, want ErrQueueFull", err)
	}
}

func TestProgressUnknownVideo(t *testing.T) {
	t.Parallel()
	p := NewProcessor(ProcessorConfig{
		Transcriber: &fakeTranscriber{},
		Matcher:     &fakeMatcher{},
		Store:       newMemStore(),
		Logger:      testLogger(),
	})
	if _, err := p.Progress(t.Context(), "nope"); !errors.Is(err, ErrUnknownVideo) {
		t.Fatalf("Progress err = %v, want ErrUnknownVideo", err)
	}
	if _, err := p.Results(t.Context(), "nope"); !errors.Is(err, ErrUnknownVideo) {
		t.Fatalf("Results err = %v, want ErrUnknownVideo", err)
	}
}

func TestPipelineCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &fakeTranscriber{segs: testSegments(3)}
		store := newMemStore()
		p := NewProcessor(ProcessorConfig{
			Transcriber: tr,
			Matcher:     &fakeMatcher{matches: testMatches()},
			Store:       store,
			Logger:      testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "https://example.com/v.mp4")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if sub.Status != StatusProcessing {
			t.Fatalf("Submit status = %q, want %q", sub.Status, StatusProcessing)
		}
		synctest.Wait()

		prog, err := p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress: %v", err)
		}
		want := Progress{VideoID: sub.VideoID, Status: StatusComplete, SegmentsTotal: 3, SegmentsDone: 3}
		if diff := cmp.Diff(want, prog); diff != "" {
			t.Errorf("progress mismatch (-want +got):\n%s", diff)
		}

		results, err := p.Results(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Results: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("got %d results, want 3", len(results))
		}
		for i, r := range results {
			if r.Start != time.Duration(i)*time.Second {
				t.Errorf("result %d start = %v, want %v", i, r.Start, time.Duration(i)*time.Second)
			}
			if diff := cmp.Diff(testMatches(), r.Matches); diff != "" {
				t.Errorf("result %d matches mismatch (-want +got):\n%s", i, diff)
			}
		}
		if !store.isProcessed(sub.VideoID) {
			t.Error("video not marked processed after completion")
		}
	})
}

func TestResubmitReturnsCachedWithoutRecompute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &fakeTranscriber{segs: testSegments(2)}
		p := NewProcessor(ProcessorConfig{
			Transcriber: tr,
			Matcher:     &fakeMatcher{matches: testMatches()},
			Store:       newMemStore(),
			Logger:      testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		again, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("re-Submit: %v", err)
		}
		if again.VideoID != sub.VideoID {
			t.Errorf("re-Submit id = %q, want %q", again.VideoID, sub.VideoID)
		}
		if again.Status != StatusComplete {
			t.Errorf("re-Submit status = %q, want %q", again.Status, StatusComplete)
		}
		synctest.Wait()
		if got := tr.callCount(); got != 1 {
			t.Errorf("transcriber called %d times, want 1 (cache must short-circuit)", got)
		}
	})
}

func TestSubmitWhileRunningDeduplicates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		tr := &fakeTranscriber{segs: testSegments(1)}
		p := NewProcessor(ProcessorConfig{
			Transcriber: tr,
			Matcher:     &fakeMatcher{matches: testMatches(), gate: gate},
			Store:       newMemStore(),
			Logger:      testLogger(),
		})
		startRun(t, p)

		if _, err := p.Submit(t.Context(), "v"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		again, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("duplicate Submit: %v", err)
		}
		if again.Status != StatusProcessing {
			t.Errorf("duplicate Submit status = %q, want %q", again.Status, StatusProcessing)
		}
		gate <- struct{}{}
		synctest.Wait()
		if got := tr.callCount(); got != 1 {
			t.Errorf("transcriber called %d times, want 1", got)
		}
	})
}

func TestProgressMidRunIsReal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		store := newMemStore()
		p := NewProcessor(ProcessorConfig{
			Transcriber: &fakeTranscriber{segs: testSegments(3)},
			Matcher:     &fakeMatcher{matches: testMatches(), gate: gate},
			Store:       store,
			Logger:      testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		gate <- struct{}{}
		synctest.Wait()

		prog, err := p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress: %v", err)
		}
		want := Progress{VideoID: sub.VideoID, Status: StatusProcessing, SegmentsTotal: 3, SegmentsDone: 1}
		if diff := cmp.Diff(want, prog); diff != "" {
			t.Errorf("mid-run progress mismatch (-want +got):\n%s", diff)
		}
		if got := store.savedCount(sub.VideoID); got != 1 {
			t.Errorf("store has %d results mid-run, want 1 (progress must reflect persisted work)", got)
		}
		if _, err := p.Results(t.Context(), sub.VideoID); !errors.Is(err, ErrResultsNotReady) {
			t.Errorf("Results mid-run err = %v, want ErrResultsNotReady", err)
		}

		gate <- struct{}{}
		gate <- struct{}{}
		synctest.Wait()
		prog, err = p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress after completion: %v", err)
		}
		if prog.Status != StatusComplete || prog.SegmentsDone != 3 {
			t.Errorf("final progress = %+v, want complete 3/3", prog)
		}
	})
}

func TestCancellationFailsInFlightJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		store := newMemStore()
		p := NewProcessor(ProcessorConfig{
			Transcriber: &fakeTranscriber{segs: testSegments(2)},
			Matcher:     &fakeMatcher{matches: testMatches(), gate: gate},
			Store:       store,
			Logger:      testLogger(),
		})
		cancel := startRun(t, p)

		sub, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		cancel()
		synctest.Wait()

		prog, err := p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress: %v", err)
		}
		if prog.Status != StatusFailed {
			t.Errorf("status after cancellation = %q, want %q", prog.Status, StatusFailed)
		}
		if store.isProcessed(sub.VideoID) {
			t.Error("canceled video must not be marked processed")
		}
		if _, err := p.Results(t.Context(), sub.VideoID); !errors.Is(err, ErrResultsNotReady) {
			t.Errorf("Results err = %v, want ErrResultsNotReady", err)
		}
	})
}

func TestFailureModes(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		configure func(tr *fakeTranscriber, m *fakeMatcher, s *memStore)
	}{
		{
			name:      "transcribe error",
			configure: func(tr *fakeTranscriber, _ *fakeMatcher, _ *memStore) { tr.err = boom },
		},
		{
			name:      "match error",
			configure: func(_ *fakeTranscriber, m *fakeMatcher, _ *memStore) { m.errOn = "segment 1" },
		},
		{
			name:      "save error",
			configure: func(_ *fakeTranscriber, _ *fakeMatcher, s *memStore) { s.saveErr = boom },
		},
		{
			name:      "mark processed error",
			configure: func(_ *fakeTranscriber, _ *fakeMatcher, s *memStore) { s.markErr = boom },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &fakeTranscriber{segs: testSegments(2)}
				m := &fakeMatcher{matches: testMatches()}
				store := newMemStore()
				tc.configure(tr, m, store)
				p := NewProcessor(ProcessorConfig{
					Transcriber: tr,
					Matcher:     m,
					Store:       store,
					Logger:      testLogger(),
				})
				startRun(t, p)

				sub, err := p.Submit(t.Context(), "v")
				if err != nil {
					t.Fatalf("Submit: %v", err)
				}
				synctest.Wait()

				prog, err := p.Progress(t.Context(), sub.VideoID)
				if err != nil {
					t.Fatalf("Progress: %v", err)
				}
				if prog.Status != StatusFailed {
					t.Fatalf("status = %q, want %q", prog.Status, StatusFailed)
				}
				if prog.Err == "" {
					t.Error("failed progress carries no error message")
				}
				if store.isProcessed(sub.VideoID) {
					t.Error("failed video must not be marked processed")
				}
				if _, err := p.Results(t.Context(), sub.VideoID); !errors.Is(err, ErrResultsNotReady) {
					t.Errorf("Results err = %v, want ErrResultsNotReady", err)
				}
			})
		})
	}
}

func TestPartialFailureThenResubmitRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &fakeTranscriber{segs: testSegments(3)}
		m := &fakeMatcher{matches: testMatches(), errOn: "segment 1"}
		store := newMemStore()
		p := NewProcessor(ProcessorConfig{
			Transcriber: tr,
			Matcher:     m,
			Store:       store,
			Logger:      testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		prog, err := p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress: %v", err)
		}
		if prog.Status != StatusFailed {
			t.Fatalf("status = %q, want failed", prog.Status)
		}
		if got := store.savedCount(sub.VideoID); got != 1 {
			t.Errorf("store has %d results after partial failure, want 1", got)
		}
		if !strings.Contains(prog.Err, "match") {
			t.Errorf("failure message %q does not name the failing step", prog.Err)
		}

		m.setErrOn("")
		again, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("re-Submit after failure: %v", err)
		}
		if again.Status != StatusProcessing {
			t.Fatalf("re-Submit status = %q, want processing (failed jobs retry)", again.Status)
		}
		synctest.Wait()

		prog, err = p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress after retry: %v", err)
		}
		if prog.Status != StatusComplete || prog.SegmentsDone != 3 {
			t.Fatalf("progress after retry = %+v, want complete 3/3", prog)
		}
		results, err := p.Results(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Results after retry: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("got %d results, want 3", len(results))
		}
	})
}

func TestReprocessingClearsStaleResults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &fakeTranscriber{segs: testSegments(2)}
		m := &fakeMatcher{matches: testMatches(), errOn: "segment 1"}
		store := newMemStore()
		p := NewProcessor(ProcessorConfig{
			Transcriber: tr,
			Matcher:     m,
			Store:       store,
			Logger:      testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()
		if got := store.savedCount(sub.VideoID); got != 1 {
			t.Fatalf("store has %d results after partial failure, want 1", got)
		}

		m.setErrOn("")
		tr.setSegs([]domain.Segment{
			{Start: 500 * time.Millisecond, End: time.Second, Text: "re-segmented a"},
			{Start: 1500 * time.Millisecond, End: 2 * time.Second, Text: "re-segmented b"},
		})
		if _, err := p.Submit(t.Context(), "v"); err != nil {
			t.Fatalf("re-Submit: %v", err)
		}
		synctest.Wait()

		results, err := p.Results(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Results: %v", err)
		}
		wantStarts := []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}
		if len(results) != len(wantStarts) {
			t.Fatalf("got %d results, want %d (stale rows from the failed run must be cleared)", len(results), len(wantStarts))
		}
		for i, r := range results {
			if r.Start != wantStarts[i] {
				t.Errorf("result %d start = %v, want %v", i, r.Start, wantStarts[i])
			}
		}
	})
}

func TestZeroSegmentVideoCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newMemStore()
		p := NewProcessor(ProcessorConfig{
			Transcriber: &fakeTranscriber{},
			Matcher:     &fakeMatcher{},
			Store:       store,
			Logger:      testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "silent")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		prog, err := p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress: %v", err)
		}
		want := Progress{VideoID: sub.VideoID, Status: StatusComplete}
		if diff := cmp.Diff(want, prog); diff != "" {
			t.Errorf("progress mismatch (-want +got):\n%s", diff)
		}
		results, err := p.Results(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Results: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("got %d results, want 0", len(results))
		}
	})
}

func TestProcessedVideoServedFromStoreAcrossRestarts(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	videoID := VideoID("v")
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "hello"}
	if err := store.SaveSegmentResult(t.Context(), videoID, domain.SegmentResult{Segment: seg, Matches: testMatches()}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := store.MarkVideoProcessed(t.Context(), videoID, 1); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	p := NewProcessor(ProcessorConfig{
		Transcriber: &fakeTranscriber{},
		Matcher:     &fakeMatcher{},
		Store:       store,
		Logger:      testLogger(),
	})

	sub, err := p.Submit(t.Context(), "v")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sub.Status != StatusComplete {
		t.Errorf("Submit status = %q, want complete (persisted cache)", sub.Status)
	}

	prog, err := p.Progress(t.Context(), videoID)
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	want := Progress{VideoID: videoID, Status: StatusComplete, SegmentsTotal: 1, SegmentsDone: 1}
	if diff := cmp.Diff(want, prog); diff != "" {
		t.Errorf("progress mismatch (-want +got):\n%s", diff)
	}

	results, err := p.Results(t.Context(), videoID)
	if err != nil {
		t.Fatalf("Results: %v", err)
	}
	if len(results) != 1 || results[0].Text != "hello" {
		t.Errorf("results = %+v, want the seeded segment", results)
	}
}

func TestPipelineSkipsUncheckableSegments(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		segs := []domain.Segment{
			{Start: 0, End: time.Second, Text: "the earth orbits the sun"},
			{Start: time.Second, End: 2 * time.Second, Text: "what do you think"},
			{Start: 2 * time.Second, End: 3 * time.Second, Text: "obscure uncovered talk"},
		}
		store := newMemStore()
		// The matcher errors on either non-checkable text, so a gate leak would
		// fail the run instead of silently matching skipped speech.
		matcher := &fakeMatcher{matches: testMatches()}
		matcher.setErrOn("what do you think")
		p := NewProcessor(ProcessorConfig{
			Transcriber: &fakeTranscriber{segs: segs},
			Matcher:     matcher,
			Prechecker: &fakePrechecker{decisions: map[string]domain.PrecheckDecision{
				"what do you think":      domain.Skipped(domain.SkipReasonNotAClaim),
				"obscure uncovered talk": domain.Skipped(domain.SkipReasonNotCovered),
			}},
			Store:  store,
			Logger: testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		results, err := p.Results(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Results: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("got %d results, want 3", len(results))
		}

		// Checked claim: matched, no skip reason.
		if results[0].SkipReason != domain.SkipReasonNone {
			t.Errorf("checked segment skip reason = %q, want none", results[0].SkipReason)
		}
		if diff := cmp.Diff(testMatches(), results[0].Matches); diff != "" {
			t.Errorf("checked segment matches mismatch (-want +got):\n%s", diff)
		}
		// Skipped segments: skip reason set, never matched.
		if results[1].SkipReason != domain.SkipReasonNotAClaim {
			t.Errorf("segment 1 skip reason = %q, want not_a_claim", results[1].SkipReason)
		}
		if results[2].SkipReason != domain.SkipReasonNotCovered {
			t.Errorf("segment 2 skip reason = %q, want not_covered", results[2].SkipReason)
		}
		for _, i := range []int{1, 2} {
			if len(results[i].Matches) != 0 {
				t.Errorf("skipped segment %d carries %d matches, want none", i, len(results[i].Matches))
			}
		}
	})
}

func TestPipelineFailsOnPrecheckError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newMemStore()
		p := NewProcessor(ProcessorConfig{
			Transcriber: &fakeTranscriber{segs: testSegments(1)},
			Matcher:     &fakeMatcher{matches: testMatches()},
			Prechecker:  &fakePrechecker{err: errors.New("retrieval down")},
			Store:       store,
			Logger:      testLogger(),
		})
		startRun(t, p)

		sub, err := p.Submit(t.Context(), "v")
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		prog, err := p.Progress(t.Context(), sub.VideoID)
		if err != nil {
			t.Fatalf("Progress: %v", err)
		}
		if prog.Status != StatusFailed {
			t.Errorf("status = %q, want failed when the precheck errors", prog.Status)
		}
		if store.isProcessed(sub.VideoID) {
			t.Error("video marked processed despite a precheck failure")
		}
	})
}
