package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// recordingStep is a PostAnalysisStep that records what it saw when it ran.
type recordingStep struct {
	err           error
	ran           []string
	persistsAtRun int
	hadDeadline   bool
	persister     *fakeVideoPersister
}

func (s *recordingStep) Run(ctx context.Context, videoID string) error {
	s.ran = append(s.ran, videoID)
	_, s.hadDeadline = ctx.Deadline()
	if s.persister != nil {
		s.persistsAtRun, _, _, _ = s.persister.snapshot()
	}
	return s.err
}

func TestVideoAnalyzerAfterCompleteRunsOnceTheAnalysisIsStored(t *testing.T) {
	t.Parallel()
	step := &recordingStep{}
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{AfterComplete: step})
	step.persister = fx.persister
	fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames(pcmFrames(3)...)}}
	fx.runner.final = storedEvents()

	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if diff := cmp.Diff([]string{"v1"}, step.ran); diff != "" {
		t.Errorf("step runs mismatch (-want +got):\n%s", diff)
	}
	if step.persistsAtRun != 1 {
		t.Errorf("the step ran before the analysis was stored (persist calls = %d)", step.persistsAtRun)
	}
	if !step.hadDeadline {
		t.Error("the step ran on an unbounded context")
	}
}

func TestVideoAnalyzerAfterCompleteFailureLeavesTheAnalysisComplete(t *testing.T) {
	t.Parallel()
	step := &recordingStep{err: errors.New("naming failed")}
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{AfterComplete: step})
	fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames(pcmFrames(3)...)}}
	fx.runner.final = storedEvents()

	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(step.ran) != 1 {
		t.Fatalf("step runs = %v, want one", step.ran)
	}
	if calls, _, _, _ := fx.persister.snapshot(); calls != 1 {
		t.Errorf("persist calls = %d, want 1", calls)
	}
	if _, _, failures := fx.store.snapshot(); len(failures) != 0 {
		t.Errorf("a failed step failed the analysis: %v", failures)
	}
}

func TestVideoAnalyzerAfterCompleteSkippedWhenTheRunFails(t *testing.T) {
	t.Parallel()
	step := &recordingStep{}
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{AfterComplete: step})
	fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames(pcmFrames(3)...)}}
	fx.runner.final = storedEvents()
	fx.persister.err = errors.New("db down")

	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(step.ran) != 0 {
		t.Errorf("the step ran after a failed analysis: %v", step.ran)
	}
	if _, _, failures := fx.store.snapshot(); len(failures) != 1 {
		t.Errorf("failures = %v, want one", failures)
	}
}

func TestNewVideoAnalyzerRejectsNegativeAfterCompleteTimeout(t *testing.T) {
	t.Parallel()
	_, err := NewVideoAnalyzer(&fakeVideoJobStore{}, &fakeAudioStreamer{}, &fakeLiveRunner{}, &fakeVideoPersister{},
		VideoAnalyzerConfig{Timeout: time.Second, AfterCompleteTimeout: -time.Second}, discardLogger())
	if err == nil {
		t.Fatal("expected an error for a negative after-complete timeout")
	}
}
