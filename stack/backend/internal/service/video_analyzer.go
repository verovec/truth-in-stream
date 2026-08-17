package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/audioextract"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// defaultProgressInterval paces the periodic analysis_progress_ms writes: often
// enough for a 2 s frontend poll to see movement, rare enough that a running
// analysis costs a negligible write load.
const defaultProgressInterval = 5 * time.Second

// AudioStream is one running audio extraction the pre-analysis job consumes:
// paced PCM frames, then a terminal error report once Frames closes. Satisfied
// by *audioextract.Stream.
type AudioStream interface {
	// Frames delivers the paced PCM frames and closes on EOF, failure, or
	// cancellation; the consumer must drain it or cancel the stream's context.
	Frames() <-chan []byte
	// Err reports why Frames closed: nil for a fully decoded stream. It is
	// valid only once Frames is closed.
	Err() error
}

// AudioStreamer opens the paced PCM audio stream for a stored video's media
// object. The job stays ignorant of ffmpeg, presigning, and chunking; the
// adapter wired in cmd/server composes those from the audioextract package.
type AudioStreamer interface {
	Stream(ctx context.Context, video domain.Video) (AudioStream, error)
}

// VideoAnalysisPersister durably stores a completed run's teed events with the
// engine fingerprint and flips the video's lifecycle to complete. Satisfied by
// *StoredAnalysisPersister, which owns the encoding and the derived claim
// counters; the job hands it raw events and never re-derives what it computes.
type VideoAnalysisPersister interface {
	Persist(ctx context.Context, videoID string, events []LiveEvent, engine json.RawMessage) (domain.VideoAnalysis, error)
}

// liveRunner is the slice of the live analyzer the job drives: audio in,
// events out, exactly the port the live WebSocket handler uses. Satisfied by
// *LiveAnalyzer.
type liveRunner interface {
	Run(ctx context.Context, audio <-chan []byte) (<-chan LiveEvent, error)
}

// videoAnalysisJobStore is the persistence slice the job lifecycle needs: the
// guarded lock claim, progress and failure writes, and startup recovery.
// *postgres.Store satisfies it structurally. The completion write goes through
// the VideoAnalysisPersister instead, which owns the atomic
// store-and-flip-complete transaction.
type videoAnalysisJobStore interface {
	StartVideoAnalysis(ctx context.Context, id string) (domain.Video, error)
	SetVideoAnalysisProgress(ctx context.Context, id string, progressMS int64) error
	FailVideoAnalysis(ctx context.Context, id, reason string) error
	RecoverInterruptedVideoAnalyses(ctx context.Context) ([]string, error)
}

// EngineMetadata is the fingerprint stamped on every stored pre-analysis: the
// model identifiers and configuration posture of the run that produced it, so
// the operator can judge whether a stored result predates a relevant change
// before deciding to re-analyse. The verify fields are empty when the verify
// path was not active (the run's verdicts then came from the legacy
// borrow-by-similarity path).
type EngineMetadata struct {
	TranscriberModel   string  `json:"transcriber_model"`
	PacingFactor       float64 `json:"pacing_factor"`
	VerifyProvider     string  `json:"verify_provider,omitempty"`
	VerifyModel        string  `json:"verify_model,omitempty"`
	RetrievalThreshold float64 `json:"retrieval_threshold,omitempty"`
	Political          bool    `json:"political,omitempty"`
	SecondPassModel    string  `json:"second_pass_model,omitempty"`
	HybridSearch       bool    `json:"hybrid_search,omitempty"`
}

// VideoAnalyzerConfig configures a VideoAnalyzer. Timeout bounds one whole run
// (a run streams at up to realtime pacing, so it must exceed the longest video
// to analyse). MaxConcurrent bounds simultaneous runs process-wide; queued
// starts hold the analysing lock with zero progress until a slot frees.
// ProgressInterval paces the periodic progress writes and defaults sensibly
// when zero. Engine is the fingerprint stamped on every stored result.
type VideoAnalyzerConfig struct {
	Timeout          time.Duration
	MaxConcurrent    int
	ProgressInterval time.Duration
	Engine           EngineMetadata
}

// VideoAnalyzer runs a ready video's stored media through the live fact-check
// pipeline as an in-process background job, mirroring DocumentAnalyzer: Start
// claims the analysing lock and a spawn-injected goroutine streams the
// extracted audio through the same LiveAnalyzer a live session uses, tees the
// emitted events, and persists them durably on completion. It holds no HTTP
// types and knows nothing about ffmpeg beyond the audio ports above.
type VideoAnalyzer struct {
	store            videoAnalysisJobStore
	audio            AudioStreamer
	analyzer         liveRunner
	persister        VideoAnalysisPersister
	engine           json.RawMessage
	timeout          time.Duration
	progressInterval time.Duration
	// slots is the global concurrency semaphore: a run holds one slot for its
	// whole duration and releases it on every exit path.
	slots  chan struct{}
	logger *slog.Logger
	// spawn runs the background worker; the real analyzer starts a goroutine,
	// tests inject a synchronous runner. Set only by the constructor and tests.
	spawn func(func())
}

// NewVideoAnalyzer builds a VideoAnalyzer. Every collaborator is required (the
// live pipeline always exists; an unusable ffmpeg surfaces as a failed run, not
// a disabled feature). MaxConcurrent defaults to 1 when zero.
func NewVideoAnalyzer(store videoAnalysisJobStore, audio AudioStreamer, analyzer liveRunner, persister VideoAnalysisPersister, cfg VideoAnalyzerConfig, logger *slog.Logger) (*VideoAnalyzer, error) {
	if store == nil {
		return nil, errors.New("video analyzer: store is required")
	}
	if audio == nil {
		return nil, errors.New("video analyzer: audio streamer is required")
	}
	if analyzer == nil {
		return nil, errors.New("video analyzer: live analyzer is required")
	}
	if persister == nil {
		return nil, errors.New("video analyzer: persister is required")
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("video analyzer: timeout must be positive, got %s", cfg.Timeout)
	}
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent == 0 {
		maxConcurrent = 1
	}
	if maxConcurrent < 0 {
		return nil, fmt.Errorf("video analyzer: max concurrent must be positive, got %d", cfg.MaxConcurrent)
	}
	progressInterval := cfg.ProgressInterval
	if progressInterval == 0 {
		progressInterval = defaultProgressInterval
	}
	if progressInterval < 0 {
		return nil, fmt.Errorf("video analyzer: progress interval must be positive, got %s", cfg.ProgressInterval)
	}
	engine, err := json.Marshal(cfg.Engine)
	if err != nil {
		return nil, fmt.Errorf("video analyzer: marshaling engine metadata: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &VideoAnalyzer{
		store:            store,
		audio:            audio,
		analyzer:         analyzer,
		persister:        persister,
		engine:           engine,
		timeout:          cfg.Timeout,
		progressInterval: progressInterval,
		slots:            make(chan struct{}, maxConcurrent),
		logger:           logger,
		spawn:            func(f func()) { go f() },
	}, nil
}

// Start triggers a video's headless pre-analysis: it claims the video under
// the analysing lock (returning domain.ErrVideoNotFound,
// domain.ErrVideoNotReady, or domain.ErrVideoAnalysisInProgress synchronously
// so the caller maps the status code), then spawns the background run. The
// same entry point re-runs a complete or failed video; the previous stored
// analysis stays readable until the new run completes and overwrites it.
func (a *VideoAnalyzer) Start(ctx context.Context, id string) error {
	video, err := a.store.StartVideoAnalysis(ctx, id)
	if err != nil {
		return err
	}
	a.spawn(func() { a.run(video) })
	return nil
}

// run executes one locked video's analysis end to end: extract, transcribe and
// verify through the live pipeline, tee the events, persist. It runs on a
// fresh, timeout-bounded context detached from the trigger request; terminal
// writes happen on their own short contexts so a run that failed because its
// context expired can still record the outcome.
//
// The slot wait is deliberately outside the run timeout: a queued start holds
// the analysing status with zero progress until a slot frees (the documented
// queueing behavior), and the wait is bounded in practice because every slot
// holder releases within its own timeout, so charging queue time against the
// queued run's own budget would only fail it spuriously.
func (a *VideoAnalyzer) run(video domain.Video) {
	a.slots <- struct{}{}
	defer func() { <-a.slots }()

	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()

	stream, err := a.audio.Stream(ctx, video)
	if err != nil {
		a.failRun(video.ID, fmt.Errorf("opening audio stream: %w", err))
		return
	}
	// From here every exit path runs the deferred cancel, which unwinds the
	// extraction (its producer watches ctx) and the tee, so no goroutine or
	// ffmpeg process outlives the run.

	tee := newAudioTee()
	audio := make(chan []byte)
	go tee.run(ctx, stream.Frames(), audio)

	events, err := a.analyzer.Run(ctx, audio)
	if err != nil {
		a.failRun(video.ID, fmt.Errorf("starting live pipeline: %w", err))
		return
	}

	progressDone := make(chan struct{})
	go a.progressLoop(ctx, video.ID, &tee.consumed, progressDone)

	var captured []LiveEvent
	for ev := range events {
		captured = append(captured, ev)
	}

	// Capture why the event stream ended before tearing anything down: after
	// cancel(), ctx.Err() is always non-nil and would misread every clean
	// completion as an interruption.
	runErr := ctx.Err()
	// Stop the progress writer and the tee before any terminal write, so a
	// periodic progress update can never land after the terminal flip and the
	// tee's completion flag is settled before it is read.
	cancel()
	<-progressDone
	<-tee.done

	switch {
	case runErr != nil:
		a.failRun(video.ID, fmt.Errorf("analysis run interrupted: %w", runErr))
	case !tee.complete:
		// The pipeline's event stream ended while audio was still flowing: the
		// transcription session died mid-run. A truncated analysis must never
		// be stored as complete.
		a.failRun(video.ID, errors.New("live pipeline ended before the audio stream finished"))
	default:
		// tee.complete means the frame channel closed, so stream.Err() is
		// settled: nil only for a fully decoded media object.
		if streamErr := stream.Err(); streamErr != nil {
			a.failRun(video.ID, fmt.Errorf("extracting audio: %w", streamErr))
			return
		}
		a.complete(video.ID, captured)
	}
}

// complete durably persists the teed events and flips the video complete, on a
// fresh short context so a run that consumed most of its budget does not fail
// at the finish line. An empty event list is rejected by the persister, so a
// run that produced nothing records a failure, never a content-free success.
//
// The completion write is deliberately not guarded by the analysing lock: a
// run that outlived a startup Recover in another process (a rolling deploy
// flipped its row to failed) still overwrites failed with complete, because
// the work genuinely finished and the stored result is real. The lock guards
// concurrent starts, not this terminal write.
func (a *VideoAnalyzer) complete(id string, events []LiveEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), failRecordTimeout)
	defer cancel()
	if _, err := a.persister.Persist(ctx, id, events, a.engine); err != nil {
		a.failRun(id, fmt.Errorf("persisting analysis: %w", err))
		return
	}
	a.logger.Info("video pre-analysis complete", slog.String("video_id", id), slog.Int("events", len(events)))
}

// failRun records a terminal failure with a clear reason on a fresh context,
// so a run that failed because its own context expired can still write it.
func (a *VideoAnalyzer) failRun(id string, cause error) {
	a.logger.Error("video pre-analysis failed", slog.String("video_id", id), slog.Any("err", cause))
	ctx, cancel := context.WithTimeout(context.Background(), failRecordTimeout)
	defer cancel()
	if err := a.store.FailVideoAnalysis(ctx, id, cause.Error()); err != nil {
		a.logger.Error("recording video analysis failure", slog.String("video_id", id), slog.Any("err", err))
	}
}

// progressLoop periodically records how much audio the pipeline has consumed,
// converted to milliseconds of video time, until ctx ends. Progress is
// accounted from delivered audio bytes (audioextract.BytesPerMilli), not event
// timestamps: bytes advance smoothly through silence and music, where a
// transcript-driven position would stall. A failed write is logged and the
// loop keeps going - progress is cosmetic, the run is not.
func (a *VideoAnalyzer) progressLoop(ctx context.Context, id string, consumed *atomic.Int64, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(a.progressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ms := consumed.Load() / audioextract.BytesPerMilli
			if err := a.store.SetVideoAnalysisProgress(ctx, id, ms); err != nil && ctx.Err() == nil {
				a.logger.WarnContext(ctx, "recording analysis progress", slog.String("video_id", id), slog.Any("err", err))
			}
		}
	}
}

// Recover flips any video left analysing by a crashed process to failed, so
// the operator can re-run it. It runs once at startup, before any Start.
func (a *VideoAnalyzer) Recover(ctx context.Context) error {
	ids, err := a.store.RecoverInterruptedVideoAnalyses(ctx)
	if err != nil {
		return fmt.Errorf("video analyzer: recover: %w", err)
	}
	if len(ids) > 0 {
		a.logger.Warn("recovered interrupted video analyses", slog.Int("count", len(ids)))
	}
	return nil
}

// audioTee forwards extracted frames to the analyzer while counting delivered
// bytes for progress accounting. complete records that the frame channel
// closed - the media was fully streamed - as opposed to the tee unwinding on a
// canceled context; it is written before done closes, so a reader that awaits
// done reads it settled.
type audioTee struct {
	consumed atomic.Int64
	complete bool
	done     chan struct{}
}

func newAudioTee() *audioTee {
	return &audioTee{done: make(chan struct{})}
}

// run pumps frames into audio until the source closes or ctx ends, closing
// audio on exit so the analyzer's stream always terminates.
func (t *audioTee) run(ctx context.Context, frames <-chan []byte, audio chan<- []byte) {
	defer close(t.done)
	defer close(audio)
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				t.complete = true
				return
			}
			select {
			case <-ctx.Done():
				return
			case audio <- frame:
				t.consumed.Add(int64(len(frame)))
			}
		}
	}
}
