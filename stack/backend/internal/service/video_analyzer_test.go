package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// videoFailure is one recorded FailVideoAnalysis call.
type videoFailure struct {
	id     string
	reason string
}

// fakeVideoJobStore is a videoAnalysisJobStore stand-in. It is mutex-guarded
// because the progress writer runs on its own goroutine.
type fakeVideoJobStore struct {
	mu          sync.Mutex
	video       domain.Video
	startErr    error
	failErr     error
	recovered   []string
	recoverErr  error
	started     []string
	progress    []int64
	failures    []videoFailure
	progressErr error
}

func (f *fakeVideoJobStore) StartVideoAnalysis(_ context.Context, id string) (domain.Video, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return domain.Video{}, f.startErr
	}
	f.started = append(f.started, id)
	video := f.video
	if video.ID == "" {
		video = domain.Video{ID: id, Status: domain.VideoStatusReady, ObjectKey: "videos/" + id}
	}
	return video, nil
}

func (f *fakeVideoJobStore) SetVideoAnalysisProgress(_ context.Context, _ string, progressMS int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.progressErr != nil {
		return f.progressErr
	}
	f.progress = append(f.progress, progressMS)
	return nil
}

func (f *fakeVideoJobStore) FailVideoAnalysis(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.failures = append(f.failures, videoFailure{id: id, reason: reason})
	return nil
}

func (f *fakeVideoJobStore) RecoverInterruptedVideoAnalyses(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recoverErr != nil {
		return nil, f.recoverErr
	}
	return f.recovered, nil
}

func (f *fakeVideoJobStore) snapshot() (started []string, progress []int64, failures []videoFailure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...), append([]int64(nil), f.progress...), append([]videoFailure(nil), f.failures...)
}

// fakeAudioStream mirrors the audioextract stream contract: frames delivers
// until the test closes it, and err must be set before that close so a reader
// that observed the close reads it settled.
type fakeAudioStream struct {
	frames chan []byte
	err    error
}

func (s *fakeAudioStream) Frames() <-chan []byte { return s.frames }
func (s *fakeAudioStream) Err() error            { return s.err }

// fakeAudioStreamer opens canned streams in order, one per Stream call, and
// records the videos it was opened for.
type fakeAudioStreamer struct {
	mu      sync.Mutex
	streams []*fakeAudioStream
	err     error
	opened  []string
}

func (f *fakeAudioStreamer) Stream(_ context.Context, video domain.Video) (AudioStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if len(f.opened) >= len(f.streams) {
		return nil, errors.New("fake streamer: no stream prepared for " + video.ID)
	}
	stream := f.streams[len(f.opened)]
	f.opened = append(f.opened, video.ID)
	return stream, nil
}

func (f *fakeAudioStreamer) openedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened)
}

// fakeLiveRunner mimics LiveAnalyzer.Run's contract: it consumes audio on its
// own goroutine and closes the event channel when audio closes (after emitting
// final), when ctx is canceled, or - simulating a dead provider session - after
// dieAfter frames, whereupon it stops consuming audio entirely.
type fakeLiveRunner struct {
	startErr error
	final    []LiveEvent
	dieAfter int
}

func (r *fakeLiveRunner) Run(ctx context.Context, audio <-chan []byte) (<-chan LiveEvent, error) {
	if r.startErr != nil {
		return nil, r.startErr
	}
	out := make(chan LiveEvent)
	go func() {
		defer close(out)
		consumed := 0
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-audio:
				if !ok {
					for _, ev := range r.final {
						select {
						case <-ctx.Done():
							return
						case out <- ev:
						}
					}
					return
				}
				consumed++
				if r.dieAfter > 0 && consumed >= r.dieAfter {
					return
				}
			}
		}
	}()
	return out, nil
}

// fakeVideoPersister is a VideoAnalysisPersister stand-in recording what the
// job hands it.
type fakeVideoPersister struct {
	mu     sync.Mutex
	err    error
	calls  int
	lastID string
	events []LiveEvent
	engine json.RawMessage
}

func (f *fakeVideoPersister) Persist(_ context.Context, videoID string, events []LiveEvent, engine json.RawMessage) (domain.VideoAnalysis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastID = videoID
	f.events = append([]LiveEvent(nil), events...)
	f.engine = engine
	if f.err != nil {
		return domain.VideoAnalysis{}, f.err
	}
	return domain.VideoAnalysis{VideoID: videoID}, nil
}

func (f *fakeVideoPersister) snapshot() (calls int, id string, events []LiveEvent, engine json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.lastID, append([]LiveEvent(nil), f.events...), f.engine
}

// closedFrames returns a frame channel preloaded with frames and already
// closed: a media object that decodes fully and immediately.
func closedFrames(frames ...[]byte) chan []byte {
	ch := make(chan []byte, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)
	return ch
}

// pcmFrames builds n standard 100 ms frames (3200 bytes at 32 bytes/ms).
func pcmFrames(n int) [][]byte {
	frames := make([][]byte, n)
	for i := range frames {
		frames[i] = make([]byte, 3200)
	}
	return frames
}

type videoAnalyzerFixture struct {
	store     *fakeVideoJobStore
	streamer  *fakeAudioStreamer
	runner    *fakeLiveRunner
	persister *fakeVideoPersister
	analyzer  *VideoAnalyzer
}

// newVideoAnalyzerFixture wires an analyzer over fresh fakes with a
// synchronous spawn, so Start returns only after the whole run finished.
func newVideoAnalyzerFixture(t *testing.T, cfg VideoAnalyzerConfig) *videoAnalyzerFixture {
	t.Helper()
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	fx := &videoAnalyzerFixture{
		store:     &fakeVideoJobStore{},
		streamer:  &fakeAudioStreamer{},
		runner:    &fakeLiveRunner{},
		persister: &fakeVideoPersister{},
	}
	analyzer, err := NewVideoAnalyzer(fx.store, fx.streamer, fx.runner, fx.persister, cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewVideoAnalyzer: %v", err)
	}
	analyzer.spawn = func(f func()) { f() }
	fx.analyzer = analyzer
	return fx
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNewVideoAnalyzerValidation(t *testing.T) {
	t.Parallel()
	store := &fakeVideoJobStore{}
	streamer := &fakeAudioStreamer{}
	runner := &fakeLiveRunner{}
	persister := &fakeVideoPersister{}
	valid := VideoAnalyzerConfig{Timeout: time.Minute}
	tests := []struct {
		name      string
		store     videoAnalysisJobStore
		audio     AudioStreamer
		analyzer  liveRunner
		persister VideoAnalysisPersister
		cfg       VideoAnalyzerConfig
		wantErr   string
	}{
		{name: "valid", store: store, audio: streamer, analyzer: runner, persister: persister, cfg: valid},
		{name: "nil store", audio: streamer, analyzer: runner, persister: persister, cfg: valid, wantErr: "store is required"},
		{name: "nil audio", store: store, analyzer: runner, persister: persister, cfg: valid, wantErr: "audio streamer is required"},
		{name: "nil analyzer", store: store, audio: streamer, persister: persister, cfg: valid, wantErr: "live analyzer is required"},
		{name: "nil persister", store: store, audio: streamer, analyzer: runner, cfg: valid, wantErr: "persister is required"},
		{name: "zero timeout", store: store, audio: streamer, analyzer: runner, persister: persister, cfg: VideoAnalyzerConfig{}, wantErr: "timeout must be positive"},
		{name: "negative max concurrent", store: store, audio: streamer, analyzer: runner, persister: persister, cfg: VideoAnalyzerConfig{Timeout: time.Minute, MaxConcurrent: -1}, wantErr: "max concurrent must be positive"},
		{name: "negative progress interval", store: store, audio: streamer, analyzer: runner, persister: persister, cfg: VideoAnalyzerConfig{Timeout: time.Minute, ProgressInterval: -time.Second}, wantErr: "progress interval must be positive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewVideoAnalyzer(tc.store, tc.audio, tc.analyzer, tc.persister, tc.cfg, discardLogger())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("NewVideoAnalyzer: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestVideoAnalyzerStartClassifiedErrors proves the trigger path surfaces the
// store's classified refusals synchronously and spawns nothing.
func TestVideoAnalyzerStartClassifiedErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "unknown video", err: domain.ErrVideoNotFound},
		{name: "not ready", err: domain.ErrVideoNotReady},
		{name: "already analysing", err: domain.ErrVideoAnalysisInProgress},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{})
			fx.store.startErr = tc.err
			spawned := false
			fx.analyzer.spawn = func(func()) { spawned = true }
			if err := fx.analyzer.Start(t.Context(), "v1"); !errors.Is(err, tc.err) {
				t.Fatalf("Start error = %v, want %v", err, tc.err)
			}
			if spawned {
				t.Error("Start spawned a run despite the refusal")
			}
		})
	}
}

// TestVideoAnalyzerRunPersistsTeedEvents is the happy path: the whole audio
// stream is consumed, every emitted event is teed in order, and the persister
// receives them with the engine fingerprint. No failure is recorded.
func TestVideoAnalyzerRunPersistsTeedEvents(t *testing.T) {
	t.Parallel()
	engine := EngineMetadata{TranscriberModel: "u3-rt-pro", PacingFactor: 1.0, VerifyProvider: "deepseek", VerifyModel: "deepseek-chat"}
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{Engine: engine})
	fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames(pcmFrames(3)...)}}
	fx.runner.final = storedEvents()

	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	calls, id, events, engineJSON := fx.persister.snapshot()
	if calls != 1 || id != "v1" {
		t.Fatalf("persist calls = %d for %q, want 1 for v1", calls, id)
	}
	if diff := cmp.Diff(storedEvents(), events); diff != "" {
		t.Errorf("persisted events mismatch (-want +got):\n%s", diff)
	}
	wantEngine, err := json.Marshal(engine)
	if err != nil {
		t.Fatalf("marshal engine: %v", err)
	}
	if string(engineJSON) != string(wantEngine) {
		t.Errorf("engine = %s, want %s", engineJSON, wantEngine)
	}
	started, _, failures := fx.store.snapshot()
	if diff := cmp.Diff([]string{"v1"}, started); diff != "" {
		t.Errorf("lock claims mismatch (-want +got):\n%s", diff)
	}
	if len(failures) != 0 {
		t.Errorf("failures = %v, want none", failures)
	}
}

// TestVideoAnalyzerRunFailures drives every terminal failure path and asserts
// the run flips failed with a reason naming the actual cause, and that the
// persister never stores a truncated or absent result.
func TestVideoAnalyzerRunFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		prepare     func(fx *videoAnalyzerFixture)
		wantReason  string
		wantPersist bool
	}{
		{
			name:       "audio stream open failure",
			prepare:    func(fx *videoAnalyzerFixture) { fx.streamer.err = errors.New("presign exploded") },
			wantReason: "opening audio stream: presign exploded",
		},
		{
			name: "live pipeline start failure",
			prepare: func(fx *videoAnalyzerFixture) {
				fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames()}}
				fx.runner.startErr = errors.New("transcriber down")
			},
			wantReason: "starting live pipeline: transcriber down",
		},
		{
			name: "extraction failed mid run",
			prepare: func(fx *videoAnalyzerFixture) {
				fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames(pcmFrames(2)...), err: errors.New("ffmpeg: corrupt media")}}
				fx.runner.final = storedEvents()
			},
			wantReason:  "extracting audio: ffmpeg: corrupt media",
			wantPersist: false,
		},
		{
			name: "provider session died before the audio finished",
			prepare: func(fx *videoAnalyzerFixture) {
				frames := make(chan []byte, 3)
				for _, f := range pcmFrames(3) {
					frames <- f
				}
				// The channel stays open: audio was still flowing when the
				// pipeline's event stream ended.
				fx.streamer.streams = []*fakeAudioStream{{frames: frames}}
				fx.runner.dieAfter = 1
			},
			wantReason: "live pipeline ended before the audio stream finished",
		},
		{
			name: "persister rejects the result",
			prepare: func(fx *videoAnalyzerFixture) {
				fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames(pcmFrames(1)...)}}
				fx.runner.final = storedEvents()
				fx.persister.err = errors.New("upsert failed")
			},
			wantReason:  "persisting analysis: upsert failed",
			wantPersist: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{})
			tc.prepare(fx)
			if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
				t.Fatalf("Start: %v", err)
			}
			_, _, failures := fx.store.snapshot()
			if len(failures) != 1 {
				t.Fatalf("failures = %v, want exactly one", failures)
			}
			if failures[0].id != "v1" || !strings.Contains(failures[0].reason, tc.wantReason) {
				t.Errorf("failure = %+v, want reason containing %q", failures[0], tc.wantReason)
			}
			calls, _, _, _ := fx.persister.snapshot()
			wantCalls := 0
			if tc.wantPersist {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Errorf("persist calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

// TestVideoAnalyzerRunTimeout proves a run that overruns its budget fails with
// an interruption reason instead of lingering analysing.
func TestVideoAnalyzerRunTimeout(t *testing.T) {
	t.Parallel()
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{Timeout: 25 * time.Millisecond})
	// The frame channel never delivers and never closes: the run can only end
	// by its timeout.
	fx.streamer.streams = []*fakeAudioStream{{frames: make(chan []byte)}}
	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, _, failures := fx.store.snapshot()
	if len(failures) != 1 || !strings.Contains(failures[0].reason, "analysis run interrupted") {
		t.Fatalf("failures = %v, want one interruption", failures)
	}
	if calls, _, _, _ := fx.persister.snapshot(); calls != 0 {
		t.Errorf("persist calls = %d, want 0", calls)
	}
}

// TestVideoAnalyzerEmptyRunFails wires the REAL stored-analysis persister to
// prove the contract end to end: a run that drained cleanly but produced no
// events records a failure, never a content-free completion.
func TestVideoAnalyzerEmptyRunFails(t *testing.T) {
	t.Parallel()
	store := &fakeVideoJobStore{}
	streamer := &fakeAudioStreamer{streams: []*fakeAudioStream{{frames: closedFrames(pcmFrames(2)...)}}}
	analysisStore := &fakeAnalysisStore{}
	persister, err := NewStoredAnalysisPersister(analysisStore)
	if err != nil {
		t.Fatalf("NewStoredAnalysisPersister: %v", err)
	}
	analyzer, err := NewVideoAnalyzer(store, streamer, &fakeLiveRunner{}, persister, VideoAnalyzerConfig{Timeout: 5 * time.Second}, discardLogger())
	if err != nil {
		t.Fatalf("NewVideoAnalyzer: %v", err)
	}
	analyzer.spawn = func(f func()) { f() }

	if err := analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, _, failures := store.snapshot()
	if len(failures) != 1 || !strings.Contains(failures[0].reason, "no events to persist") {
		t.Fatalf("failures = %v, want one empty-run rejection", failures)
	}
	if analysisStore.lastPersist.VideoID != "" {
		t.Errorf("stored analysis = %+v, want none", analysisStore.lastPersist)
	}
}

// TestVideoAnalyzerLateCompletionOverwritesRecoveredFailure documents the
// recover-vs-late-completion decision: the job never re-checks the lifecycle
// before persisting, so a run that outlived a startup Recover in another
// process (which flipped its row to failed) still completes - the work
// genuinely finished, and the store's unguarded CompleteVideoAnalysis lets the
// finished result overwrite the failed flip.
func TestVideoAnalyzerLateCompletionOverwritesRecoveredFailure(t *testing.T) {
	t.Parallel()
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{})
	// The video the lock claim returns is already stamped failed, as if a
	// concurrent Recover had flipped it mid-run; the job must not care.
	fx.store.video = domain.Video{ID: "v1", Status: domain.VideoStatusReady, AnalysisStatus: domain.VideoAnalysisFailed}
	fx.streamer.streams = []*fakeAudioStream{{frames: closedFrames(pcmFrames(1)...)}}
	fx.runner.final = storedEvents()

	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	calls, id, _, _ := fx.persister.snapshot()
	if calls != 1 || id != "v1" {
		t.Fatalf("persist calls = %d for %q, want the completion to be persisted", calls, id)
	}
	if _, _, failures := fx.store.snapshot(); len(failures) != 0 {
		t.Errorf("failures = %v, want none from the job itself", failures)
	}
}

// TestVideoAnalyzerProgressFromConsumedAudio proves progress is accounted from
// delivered audio bytes at audioextract's 32 bytes/ms, surviving in
// analysis_progress_ms while the run is still going.
func TestVideoAnalyzerProgressFromConsumedAudio(t *testing.T) {
	t.Parallel()
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{ProgressInterval: time.Millisecond})
	frames := make(chan []byte, 32)
	for _, f := range pcmFrames(32) {
		frames <- f
	}
	fx.streamer.streams = []*fakeAudioStream{{frames: frames}}
	fx.runner.final = storedEvents()
	fx.analyzer.spawn = func(f func()) { go f() }

	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 32 frames x 3200 bytes = 102400 bytes = 3200 ms of audio at 32 bytes/ms.
	waitFor(t, "a progress write at 3200 ms", func() bool {
		_, progress, _ := fx.store.snapshot()
		for _, ms := range progress {
			if ms == 3200 {
				return true
			}
		}
		return false
	})
	close(frames)
	waitFor(t, "the run to complete", func() bool {
		calls, _, _, _ := fx.persister.snapshot()
		return calls == 1
	})
}

// TestVideoAnalyzerConcurrencyCap proves the global semaphore: with one slot,
// a second queued run does not even open its audio stream until the first run
// finished, then proceeds to completion.
func TestVideoAnalyzerConcurrencyCap(t *testing.T) {
	t.Parallel()
	fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{MaxConcurrent: 1})
	first := &fakeAudioStream{frames: make(chan []byte)}
	second := &fakeAudioStream{frames: closedFrames(pcmFrames(1)...)}
	fx.streamer.streams = []*fakeAudioStream{first, second}
	fx.runner.final = storedEvents()
	fx.analyzer.spawn = func(f func()) { go f() }

	if err := fx.analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start v1: %v", err)
	}
	waitFor(t, "the first stream to open", func() bool { return fx.streamer.openedCount() == 1 })
	if err := fx.analyzer.Start(t.Context(), "v2"); err != nil {
		t.Fatalf("Start v2: %v", err)
	}
	// The queued run holds the analysing lock but must not start extracting.
	time.Sleep(20 * time.Millisecond)
	if got := fx.streamer.openedCount(); got != 1 {
		t.Fatalf("streams opened while the slot was held = %d, want 1", got)
	}
	// Finish the first run: its media reaches EOF, it persists, the slot frees.
	close(first.frames)
	waitFor(t, "both runs to persist", func() bool {
		calls, _, _, _ := fx.persister.snapshot()
		return calls == 2
	})
	if got := fx.streamer.openedCount(); got != 2 {
		t.Errorf("streams opened = %d, want 2", got)
	}
}

func TestVideoAnalyzerRecover(t *testing.T) {
	t.Parallel()
	t.Run("flips orphans and reports success", func(t *testing.T) {
		t.Parallel()
		fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{})
		fx.store.recovered = []string{"v1", "v2"}
		if err := fx.analyzer.Recover(t.Context()); err != nil {
			t.Fatalf("Recover: %v", err)
		}
	})
	t.Run("wraps a store failure", func(t *testing.T) {
		t.Parallel()
		fx := newVideoAnalyzerFixture(t, VideoAnalyzerConfig{})
		fx.store.recoverErr = errors.New("db down")
		err := fx.analyzer.Recover(t.Context())
		if err == nil || !strings.Contains(err.Error(), "video analyzer: recover: db down") {
			t.Fatalf("Recover error = %v, want a wrapped db failure", err)
		}
	})
}

// TestVideoAnalyzerStoredRunServesReplaySeam is the unit-level export/WS
// fast-path check: events the job persists through the real persister come
// back verbatim through the stored-analysis reader and the composite replayer
// - the exact seam the live socket's replay path and the SRT/CSV exports read.
func TestVideoAnalyzerStoredRunServesReplaySeam(t *testing.T) {
	t.Parallel()
	analysisStore := &fakeAnalysisStore{}
	persister, err := NewStoredAnalysisPersister(analysisStore)
	if err != nil {
		t.Fatalf("NewStoredAnalysisPersister: %v", err)
	}
	store := &fakeVideoJobStore{}
	streamer := &fakeAudioStreamer{streams: []*fakeAudioStream{{frames: closedFrames(pcmFrames(1)...)}}}
	runner := &fakeLiveRunner{final: storedEvents()}
	analyzer, err := NewVideoAnalyzer(store, streamer, runner, persister, VideoAnalyzerConfig{Timeout: 5 * time.Second}, discardLogger())
	if err != nil {
		t.Fatalf("NewVideoAnalyzer: %v", err)
	}
	analyzer.spawn = func(f func()) { f() }
	if err := analyzer.Start(t.Context(), "v1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, _, failures := store.snapshot(); len(failures) != 0 {
		t.Fatalf("failures = %v, want none", failures)
	}

	// Serve the stored row back through the reader and the composite replayer,
	// exactly as wiring does for the live socket and the export endpoints.
	analysisStore.stored = analysisStore.lastPersist
	reader, err := NewStoredAnalysisReader(&fakeVideoGetter{video: domain.Video{ID: "v1"}}, analysisStore, discardLogger())
	if err != nil {
		t.Fatalf("NewStoredAnalysisReader: %v", err)
	}
	replayer, err := NewCompositeReplayer(discardLogger(), reader)
	if err != nil {
		t.Fatalf("NewCompositeReplayer: %v", err)
	}
	events, found, err := replayer.Snapshot(t.Context(), "v1")
	if err != nil || !found {
		t.Fatalf("Snapshot found=%v err=%v, want a hit", found, err)
	}
	if diff := cmp.Diff(storedEvents(), events); diff != "" {
		t.Errorf("replayed events mismatch (-want +got):\n%s", diff)
	}
}

// TestEngineMetadataJSONShape pins the stored engine fingerprint's wire shape,
// including which zero-valued fields are omitted.
func TestEngineMetadataJSONShape(t *testing.T) {
	t.Parallel()
	full := EngineMetadata{
		TranscriberModel:   "u3-rt-pro",
		PacingFactor:       1,
		VerifyProvider:     "deepseek",
		VerifyModel:        "deepseek-chat",
		RetrievalThreshold: 0.45,
		KnowledgeFallback:  true,
		Political:          true,
		SecondPassModel:    "deepseek-reasoner",
		HybridSearch:       true,
	}
	got, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"transcriber_model":"u3-rt-pro","pacing_factor":1,"verify_provider":"deepseek","verify_model":"deepseek-chat","retrieval_threshold":0.45,"knowledge_fallback":true,"political":true,"second_pass_model":"deepseek-reasoner","hybrid_search":true}`
	if string(got) != want {
		t.Errorf("engine json = %s, want %s", got, want)
	}
	legacy, err := json.Marshal(EngineMetadata{TranscriberModel: "u3-rt-pro", PacingFactor: 0.5})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantLegacy := `{"transcriber_model":"u3-rt-pro","pacing_factor":0.5}`
	if string(legacy) != wantLegacy {
		t.Errorf("legacy engine json = %s, want %s", legacy, wantLegacy)
	}
}
