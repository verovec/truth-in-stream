// Package embedjob is the embedding-worker consumer logic: it drains embedding
// jobs from the priority queue, embeds each chunk's text through the Voyage
// embedder, and writes the vector into the staging corpus. It is transport-free
// - it depends on its own small Stream/Delivery/Enqueuer interfaces, never on a
// concrete broker or any HTTP type - so the worker is unit-testable and the
// broker is swappable behind the cmd-layer adapters.
package embedjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Job is one unit of embedding work: the chunk to embed, identified by its
// corpus position, the text to embed, and the delivery attempt so far. The
// producer sets Attempt to zero; the worker increments it when it re-enqueues a
// transient failure, so a job that keeps failing is eventually dropped rather
// than looping forever.
type Job struct {
	PageID     int64  `json:"page_id"`
	ChunkIndex int    `json:"chunk_index"`
	Content    string `json:"content"`
	Attempt    int    `json:"attempt,omitzero"`
}

// validate rejects a job that can never succeed, so the worker drops it instead
// of embedding nonsense or looping. Wikipedia page ids are positive and chunk
// indices are non-negative; empty content has nothing to embed; a negative
// attempt is a corrupt or overflowed counter that must not be retried.
func (j Job) validate() error {
	switch {
	case j.PageID <= 0:
		return fmt.Errorf("page id %d must be positive", j.PageID)
	case j.ChunkIndex < 0:
		return fmt.Errorf("chunk index %d must not be negative", j.ChunkIndex)
	case j.Content == "":
		return fmt.Errorf("page %d chunk %d has empty content", j.PageID, j.ChunkIndex)
	case j.Attempt < 0:
		return fmt.Errorf("page %d chunk %d has a negative attempt %d", j.PageID, j.ChunkIndex, j.Attempt)
	default:
		return nil
	}
}

// Action is what the consume loop must do with a delivery after Process decides
// the job's fate.
type Action int

const (
	// ActionAck drops the delivery: the job was handled, was obsolete, or can
	// never succeed (a poison message or one past its retry budget).
	ActionAck Action = iota
	// ActionRepublish re-enqueues the job (with its attempt incremented) for a
	// bounded retry, then drops the original.
	ActionRepublish
	// ActionRequeue returns the delivery to the broker unhandled because a
	// shutdown cut the work short, so it is redelivered without burning an attempt.
	ActionRequeue
)

// Result is the outcome of processing one message: the broker action plus, for
// ActionRepublish, the re-enqueued job body and the priority to preserve.
type Result struct {
	Action            Action
	RepublishBody     []byte
	RepublishPriority uint8
}

// Embedder embeds chunk text for storage. The Voyage *embed.Client wrapped in
// its retry and rate-limit decorators satisfies it; the worker only ever embeds
// documents, never queries.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// Store writes a chunk's embedding into the staging corpus, keyed on identity.
// updated is false when no staging row matches - the chunk left staging or the
// corpus was already swapped live - so the worker can drop an obsolete job
// rather than retry it. Writes are idempotent: a redelivered job rewrites the
// same vector safely.
type Store interface {
	SetStagingChunkEmbedding(ctx context.Context, pageID int64, chunkIndex int, embedding []float32) (updated bool, err error)
}

// Delivery is one job message awaiting acknowledgement, abstracting the broker
// so the worker stays transport-free. The consumer MUST eventually Ack or Nack
// each delivery. Version is the queue schema version the producer stamped; the
// worker drops a delivery whose version it does not recognize.
type Delivery interface {
	Body() []byte
	Priority() uint8
	Version() string
	Ack() error
	Nack(requeue bool) error
}

// Stream yields deliveries until ctx is canceled, then closes the channel.
type Stream interface {
	Consume(ctx context.Context) (<-chan Delivery, error)
}

// Enqueuer re-enqueues a job body at the given priority for a bounded retry.
type Enqueuer interface {
	Enqueue(ctx context.Context, body []byte, priority uint8) error
}

// Config tunes a Worker. Concurrency caps the jobs one replica embeds in
// parallel (the fleet scales by replica count); MaxAttempts is the total
// delivery budget for a job before a persistent failure is dropped with a log.
// KnownVersions is the set of queue schema versions this worker understands: a
// delivery stamped with any other version is dropped rather than mis-processed.
// An empty KnownVersions disables the check (every version is accepted), which
// keeps a worker that does not configure versions working unchanged.
type Config struct {
	Concurrency   int
	MaxAttempts   int
	KnownVersions []string
}

// Worker drains embedding jobs and writes their vectors into staging.
type Worker struct {
	embedder      Embedder
	store         Store
	stream        Stream
	enqueuer      Enqueuer
	logger        *slog.Logger
	concurrency   int
	maxAttempts   int
	knownVersions map[string]struct{}
}

// NewWorker builds a Worker. Concurrency and MaxAttempts below one are clamped
// to one (a worker must process at least one job at a time and try it at least
// once), and a nil logger falls back to the default so the worker always has one.
func NewWorker(embedder Embedder, store Store, stream Stream, enqueuer Enqueuer, logger *slog.Logger, cfg Config) *Worker {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	var known map[string]struct{}
	if len(cfg.KnownVersions) > 0 {
		known = make(map[string]struct{}, len(cfg.KnownVersions))
		for _, v := range cfg.KnownVersions {
			known[v] = struct{}{}
		}
	}
	return &Worker{
		embedder:      embedder,
		store:         store,
		stream:        stream,
		enqueuer:      enqueuer,
		logger:        logger,
		concurrency:   cfg.Concurrency,
		maxAttempts:   cfg.MaxAttempts,
		knownVersions: known,
	}
}

// knowsVersion reports whether the worker should process a delivery stamped with
// version. With no configured versions the check is disabled and every version
// is accepted; otherwise only a configured version is.
func (w *Worker) knowsVersion(version string) bool {
	if w.knownVersions == nil {
		return true
	}
	_, ok := w.knownVersions[version]
	return ok
}

// Run consumes the queue until ctx is canceled, processing up to Concurrency
// jobs in parallel. It returns once it stops admitting work - the delivery
// stream closed or ctx was canceled - and every in-flight handler has finished.
// On shutdown a handler that finished acks its work; one still embedding or
// writing sees the canceled context and leaves its delivery unacknowledged, and
// any delivery already pulled from the stream but not yet handed to a handler is
// dropped unacknowledged too - the broker redelivers all of them, so a
// scale-down loses nothing (at the cost of re-embedding the interrupted few,
// which the idempotent write makes safe).
func (w *Worker) Run(ctx context.Context) error {
	deliveries, err := w.stream.Consume(ctx)
	if err != nil {
		return fmt.Errorf("embedjob: start consumer: %w", err)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, w.concurrency)
loop:
	for d := range deliveries {
		// Take a free slot immediately when one is available, so a delivery
		// already pulled from the stream is always handled (its handler requeues
		// it if ctx is already canceled). Only when every slot is busy do we wait,
		// and there a canceled ctx stops us admitting work rather than blocking on
		// a slow in-flight embed - the broker redelivers this unhandled delivery.
		select {
		case sem <- struct{}{}:
		default:
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break loop
			}
		}
		wg.Add(1)
		go func(d Delivery) {
			defer wg.Done()
			defer func() { <-sem }()
			w.handle(ctx, d)
		}(d)
	}
	wg.Wait()
	return nil
}

// handle processes one delivery and applies the broker action Process chose. A
// failed republish requeues the original rather than acking it, so a transient
// failure can never silently drop the job. A delivery whose schema version the
// worker does not understand is dropped (acked) with an ERROR log before any
// embedding, so a stray message from another queue version is discarded rather
// than mis-processed. This mirrors how Process drops a malformed or invalid job:
// a message that cannot be processed is removed, not requeued into a loop. In
// normal operation it never fires - each worker consumes only its own versioned
// queue and the producer stamps that version - so a hit means a corrupt or
// misrouted message, which the ERROR log makes visible.
func (w *Worker) handle(ctx context.Context, d Delivery) {
	if !w.knowsVersion(d.Version()) {
		w.logger.ErrorContext(ctx, "dropping embedding job with unknown queue version",
			slog.String("version", d.Version()))
		w.ack(ctx, d)
		return
	}
	res := w.Process(ctx, d.Body(), d.Priority())
	switch res.Action {
	case ActionRepublish:
		if err := w.enqueuer.Enqueue(ctx, res.RepublishBody, res.RepublishPriority); err != nil {
			w.logger.ErrorContext(ctx, "re-enqueue failed, requeuing original delivery", slog.Any("err", err))
			w.nack(ctx, d, true)
			return
		}
		w.ack(ctx, d)
	case ActionRequeue:
		w.nack(ctx, d, true)
	default:
		w.ack(ctx, d)
	}
}

func (w *Worker) ack(ctx context.Context, d Delivery) {
	if err := d.Ack(); err != nil {
		w.logger.ErrorContext(ctx, "ack failed", slog.Any("err", err))
	}
}

func (w *Worker) nack(ctx context.Context, d Delivery, requeue bool) {
	if err := d.Nack(requeue); err != nil {
		w.logger.ErrorContext(ctx, "nack failed", slog.Any("err", err), slog.Bool("requeue", requeue))
	}
}

// Process embeds the job in body and writes its vector, returning the action the
// caller must take on the delivery. It never returns an error: a malformed or
// invalid message and a persistent failure are both folded into ActionAck (after
// an ERROR log, so the drop is visible, not silent), a transient failure into
// ActionRepublish, and a shutdown into ActionRequeue. The consume loop's only
// job is to apply the action.
func (w *Worker) Process(ctx context.Context, body []byte, priority uint8) Result {
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		w.logger.ErrorContext(ctx, "dropping malformed embedding job", slog.Any("err", err))
		return Result{Action: ActionAck}
	}
	if err := job.validate(); err != nil {
		w.logger.ErrorContext(ctx, "dropping invalid embedding job", slog.Any("err", err))
		return Result{Action: ActionAck}
	}

	embeddings, err := w.embedder.EmbedDocuments(ctx, []string{job.Content})
	if err != nil {
		return w.afterFailure(ctx, job, priority, "embed", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != domain.EmbeddingDim {
		// A wrong shape is the provider breaking its contract, not a transient
		// fault: re-embedding the same content would reproduce it, so drop rather
		// than loop, and never write a malformed vector into the corpus.
		got := 0
		if len(embeddings) == 1 {
			got = len(embeddings[0])
		}
		w.logger.ErrorContext(ctx, "dropping embedding job with unexpected provider response",
			slog.Int64("page_id", job.PageID),
			slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("vectors", len(embeddings)),
			slog.Int("dims", got),
			slog.Int("want_dims", domain.EmbeddingDim))
		return Result{Action: ActionAck}
	}

	updated, err := w.store.SetStagingChunkEmbedding(ctx, job.PageID, job.ChunkIndex, embeddings[0])
	if err != nil {
		return w.afterFailure(ctx, job, priority, "write", err)
	}
	if !updated {
		w.logger.InfoContext(ctx, "embedding job references a chunk no longer in staging, dropping",
			slog.Int64("page_id", job.PageID),
			slog.Int("chunk_index", job.ChunkIndex))
		return Result{Action: ActionAck}
	}
	return Result{Action: ActionAck}
}

// afterFailure decides what to do with a job whose embed or write failed. A
// canceled context means a shutdown cut the work short: requeue it so it is
// redelivered without counting the attempt. Otherwise the attempt counts: if the
// budget is spent the job is dropped with an ERROR log so the loss is visible;
// if attempts remain it is re-enqueued with the attempt incremented, at the same
// priority so an important chunk keeps its place.
func (w *Worker) afterFailure(ctx context.Context, job Job, priority uint8, stage string, cause error) Result {
	if ctx.Err() != nil {
		w.logger.InfoContext(ctx, "embedding job interrupted by shutdown, requeuing",
			slog.String("stage", stage),
			slog.Int64("page_id", job.PageID),
			slog.Int("chunk_index", job.ChunkIndex))
		return Result{Action: ActionRequeue}
	}

	// Compare the incoming attempt against the budget rather than Attempt+1, so a
	// corrupt or crafted job with a near-max attempt cannot overflow past the cap
	// and loop forever; validate already rejected a negative attempt.
	if job.Attempt >= w.maxAttempts-1 {
		w.logger.ErrorContext(ctx, "dropping embedding job after exhausting retries",
			slog.String("stage", stage),
			slog.Int64("page_id", job.PageID),
			slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("attempt", job.Attempt),
			slog.Int("max_attempts", w.maxAttempts),
			slog.Any("err", cause))
		return Result{Action: ActionAck}
	}

	retry := job
	retry.Attempt = job.Attempt + 1
	encoded, err := json.Marshal(retry)
	if err != nil {
		// Marshaling a value just unmarshaled cannot realistically fail; if it
		// does, dropping is safer than spinning on a job that can never re-enqueue.
		w.logger.ErrorContext(ctx, "dropping embedding job that cannot be re-encoded for retry",
			slog.Int64("page_id", job.PageID),
			slog.Int("chunk_index", job.ChunkIndex),
			slog.Any("err", err))
		return Result{Action: ActionAck}
	}
	w.logger.WarnContext(ctx, "embedding job failed, re-enqueuing for retry",
		slog.String("stage", stage),
		slog.Int64("page_id", job.PageID),
		slog.Int("chunk_index", job.ChunkIndex),
		slog.Int("next_attempt", retry.Attempt),
		slog.Int("max_attempts", w.maxAttempts),
		slog.Any("err", cause))
	return Result{Action: ActionRepublish, RepublishBody: encoded, RepublishPriority: priority}
}
