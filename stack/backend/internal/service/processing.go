package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Transcriber is the slice of the VER-7 transcription service the processing
// pipeline consumes: ordered, timestamped transcript segments for a video
// source. Defined here, on the consumer side, so this package does not depend
// on the transcription implementation.
type Transcriber interface {
	Transcribe(ctx context.Context, source string) ([]domain.Segment, error)
}

// SegmentMatcher is the slice of the VER-9 embed-and-match service the
// pipeline consumes: ranked claim matches for one segment's text, most
// similar first.
type SegmentMatcher interface {
	Match(ctx context.Context, text string) ([]domain.SegmentMatch, error)
}

// SegmentPrechecker is the check-worthiness gate the pipeline consults before
// matching: it decides whether a segment is a checkable, corpus-covered claim
// worth a verdict, or one to skip with a reason. Skipped segments never reach
// the matcher, so no verdict is ever emitted on un-checkable speech.
type SegmentPrechecker interface {
	Evaluate(ctx context.Context, text string) (domain.PrecheckDecision, error)
}

// JobStatus is the lifecycle state of a processing job.
type JobStatus string

const (
	// StatusProcessing means the pipeline is queued or running.
	StatusProcessing JobStatus = "processing"
	// StatusComplete means every segment result is persisted.
	StatusComplete JobStatus = "complete"
	// StatusFailed means the pipeline stopped before completion; resubmitting
	// the video retries it.
	StatusFailed JobStatus = "failed"
)

// Sentinel errors the handler layer maps to HTTP status codes.
var (
	// ErrEmptySource rejects a submission without a video source.
	ErrEmptySource = errors.New("empty video source")
	// ErrQueueFull means the processing queue cannot take another job.
	ErrQueueFull = errors.New("processing queue full")
	// ErrUnknownVideo means the video id has never been submitted.
	ErrUnknownVideo = errors.New("unknown video")
	// ErrResultsNotReady means processing has not completed successfully, so
	// no results are served (partial results are never presented as complete).
	ErrResultsNotReady = errors.New("results not ready")
)

// Submission is the outcome of submitting a video for processing.
type Submission struct {
	VideoID string
	Status  JobStatus
}

// Progress reports how far processing has come. SegmentsDone counts only
// segments whose results are persisted, so the number never overstates the
// recoverable work. Err carries the failure message when Status is failed.
type Progress struct {
	VideoID       string
	Status        JobStatus
	SegmentsTotal int
	SegmentsDone  int
	Err           string
}

// VideoID derives the stable processing id for a video source: the SHA-256
// hex digest of the source identifier. The same source always maps to the
// same id, which is what makes result caching idempotent.
func VideoID(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// defaultQueueSize bounds how many distinct videos may wait for processing.
const defaultQueueSize = 16

// ProcessorConfig wires a Processor. Transcriber, Matcher, and Store are
// required; Logger defaults to slog.Default, QueueSize to defaultQueueSize, and
// Prechecker to a no-op gate that checks every segment.
type ProcessorConfig struct {
	Transcriber Transcriber
	Matcher     SegmentMatcher
	Store       domain.SegmentResultStore
	Prechecker  SegmentPrechecker
	Logger      *slog.Logger
	QueueSize   int
}

// job is the mutable in-memory state of one processing run, guarded by the
// processor mutex.
type job struct {
	videoID string
	source  string
	status  JobStatus
	total   int
	done    int
	errMsg  string
}

// Processor runs the full fact-check pipeline (transcribe, then embed-and-
// match each segment) once per video and serves progress and cached results.
// Completed results live in the store; in-flight job state lives in memory,
// so an interrupted run is simply reprocessed on resubmit.
type Processor struct {
	transcriber Transcriber
	matcher     SegmentMatcher
	prechecker  SegmentPrechecker
	store       domain.SegmentResultStore
	logger      *slog.Logger

	queue chan *job

	mu   sync.Mutex
	jobs map[string]*job
}

// NewProcessor builds a Processor from cfg. Call Run on a dedicated goroutine
// to start consuming submitted jobs.
func NewProcessor(cfg ProcessorConfig) *Processor {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	prechecker := cfg.Prechecker
	if prechecker == nil {
		prechecker = allowAllPrechecker{}
	}
	return &Processor{
		transcriber: cfg.Transcriber,
		matcher:     cfg.Matcher,
		prechecker:  prechecker,
		store:       cfg.Store,
		logger:      logger,
		queue:       make(chan *job, queueSize),
		jobs:        make(map[string]*job),
	}
}

// Run consumes submitted jobs until ctx is canceled. Jobs run one at a time
// off the request goroutine; cancellation stops between segments, so graceful
// shutdown is never blocked by a long video.
func (p *Processor) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-p.queue:
			p.process(ctx, j)
		}
	}
}

// Submit registers source for processing and returns the id to poll. An
// already-processed video returns StatusComplete immediately, an in-flight
// one returns its current status, and a previously failed one is retried.
func (p *Processor) Submit(ctx context.Context, source string) (Submission, error) {
	if source == "" {
		return Submission{}, fmt.Errorf("service: submit: %w", ErrEmptySource)
	}
	videoID := VideoID(source)

	_, processed, err := p.store.ProcessedSegmentCount(ctx, videoID)
	if err != nil {
		return Submission{}, fmt.Errorf("service: submit %s: %w", videoID, err)
	}
	if processed {
		return Submission{VideoID: videoID, Status: StatusComplete}, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if j, ok := p.jobs[videoID]; ok && j.status != StatusFailed {
		return Submission{VideoID: videoID, Status: j.status}, nil
	}
	j := &job{videoID: videoID, source: source, status: StatusProcessing}
	select {
	case p.queue <- j:
		p.jobs[videoID] = j
		return Submission{VideoID: videoID, Status: StatusProcessing}, nil
	default:
		return Submission{}, fmt.Errorf("service: submit %s: %w", videoID, ErrQueueFull)
	}
}

// Progress returns the current progress for videoID, falling back to the
// store for videos processed before this process started.
func (p *Processor) Progress(ctx context.Context, videoID string) (Progress, error) {
	p.mu.Lock()
	if j, ok := p.jobs[videoID]; ok {
		prog := Progress{
			VideoID:       j.videoID,
			Status:        j.status,
			SegmentsTotal: j.total,
			SegmentsDone:  j.done,
			Err:           j.errMsg,
		}
		p.mu.Unlock()
		return prog, nil
	}
	p.mu.Unlock()

	count, processed, err := p.store.ProcessedSegmentCount(ctx, videoID)
	if err != nil {
		return Progress{}, fmt.Errorf("service: progress %s: %w", videoID, err)
	}
	if !processed {
		return Progress{}, fmt.Errorf("service: progress %s: %w", videoID, ErrUnknownVideo)
	}
	return Progress{
		VideoID:       videoID,
		Status:        StatusComplete,
		SegmentsTotal: count,
		SegmentsDone:  count,
	}, nil
}

// Results returns the persisted segment results for a fully processed video,
// ordered by segment start time. A known but unfinished video yields
// ErrResultsNotReady; an unknown one yields ErrUnknownVideo.
func (p *Processor) Results(ctx context.Context, videoID string) ([]domain.SegmentResult, error) {
	_, processed, err := p.store.ProcessedSegmentCount(ctx, videoID)
	if err != nil {
		return nil, fmt.Errorf("service: results %s: %w", videoID, err)
	}
	if processed {
		results, err := p.store.ListSegmentResults(ctx, videoID)
		if err != nil {
			return nil, fmt.Errorf("service: results %s: %w", videoID, err)
		}
		return results, nil
	}

	p.mu.Lock()
	_, known := p.jobs[videoID]
	p.mu.Unlock()
	if !known {
		return nil, fmt.Errorf("service: results %s: %w", videoID, ErrUnknownVideo)
	}
	return nil, fmt.Errorf("service: results %s: %w", videoID, ErrResultsNotReady)
}

// process runs the pipeline for one job, persisting each segment's result as
// it completes so reported progress always reflects durable work.
func (p *Processor) process(ctx context.Context, j *job) {
	segments, err := p.transcriber.Transcribe(ctx, j.source)
	if err != nil {
		p.fail(ctx, j, fmt.Errorf("transcribe: %w", err))
		return
	}
	p.setTotal(j, len(segments))

	if err := p.store.DeleteSegmentResults(ctx, j.videoID); err != nil {
		p.fail(ctx, j, fmt.Errorf("clear previous results: %w", err))
		return
	}

	for _, seg := range segments {
		if err := ctx.Err(); err != nil {
			p.fail(ctx, j, fmt.Errorf("processing interrupted: %w", err))
			return
		}
		result, err := p.checkSegment(ctx, seg)
		if err != nil {
			p.fail(ctx, j, fmt.Errorf("segment at %s: %w", seg.Start, err))
			return
		}
		if err := p.store.SaveSegmentResult(ctx, j.videoID, result); err != nil {
			p.fail(ctx, j, fmt.Errorf("save segment at %s: %w", seg.Start, err))
			return
		}
		p.markSegmentDone(j)
	}

	if err := p.store.MarkVideoProcessed(ctx, j.videoID, len(segments)); err != nil {
		p.fail(ctx, j, fmt.Errorf("mark processed: %w", err))
		return
	}
	p.complete(j)
	p.logger.InfoContext(ctx, "video processed",
		slog.String("video_id", j.videoID),
		slog.Int("segments", len(segments)))
}

// checkSegment runs the precheck gate, then matches only segments worth
// checking. A skipped segment carries its skip reason and no matches, so it is
// stored and served as "not checked" rather than as a verdict; a checked one
// carries its ranked matches.
//
// A segment that clears the gate is embedded twice: once by the coverage stage
// and once by the matcher. That is the accepted cost of keeping coverage and
// matching as independent stages with their own thresholds - the cheap
// claim-worthiness stage already drops most non-claims at zero embedding cost,
// so the second embed only lands on genuine, covered claims. The batch pipeline
// is not latency-bound; the realtime path can thread the query vector through
// if profiling shows it matters.
func (p *Processor) checkSegment(ctx context.Context, seg domain.Segment) (domain.SegmentResult, error) {
	decision, err := p.prechecker.Evaluate(ctx, seg.Text)
	if err != nil {
		return domain.SegmentResult{}, fmt.Errorf("precheck: %w", err)
	}
	if !decision.Checkable {
		return domain.SegmentResult{Segment: seg, SkipReason: decision.Reason}, nil
	}

	matches, err := p.matcher.Match(ctx, seg.Text)
	if err != nil {
		return domain.SegmentResult{}, fmt.Errorf("match: %w", err)
	}
	return domain.SegmentResult{Segment: seg, Matches: matches}, nil
}

func (p *Processor) setTotal(j *job, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j.total = total
}

func (p *Processor) markSegmentDone(j *job) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j.done++
}

// complete drops the finished job from the in-memory map: the store now
// answers for it (Submit, Progress, and Results all consult the completion
// marker first), and keeping every completed video would grow the map without
// bound.
func (p *Processor) complete(j *job) {
	p.mu.Lock()
	defer p.mu.Unlock()
	j.status = StatusComplete
	delete(p.jobs, j.videoID)
}

func (p *Processor) fail(ctx context.Context, j *job, err error) {
	p.mu.Lock()
	j.status = StatusFailed
	j.errMsg = err.Error()
	p.mu.Unlock()
	p.logger.ErrorContext(ctx, "video processing failed",
		slog.String("video_id", j.videoID),
		slog.Any("err", err))
}
