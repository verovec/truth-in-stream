// Package evidencejob is the generic evidence-corpus consumer: it drains the
// self-contained connector.EvidenceJob bodies any source-adapter publishes for
// the evidence_chunks corpus, embeds each chunk's text through the Voyage API,
// and upserts the completed chunk into the live corpus. It is the drain the
// source-connector framework's [connector.EvidenceJob] was designed for - the
// source-neutral counterpart to the wiki-specific crawl worker - so a new
// evidence source is a producer plus a registry entry, with no new consumer.
//
// It is transport-free: it depends on its own small Delivery/Stream/Enqueuer
// interfaces, the Embedder, and the chunk Store, never on a concrete broker or
// any HTTP type, so the worker is unit-testable and the broker is swappable
// behind the cmd-layer adapters. It mirrors internal/crawljob's broker/retry
// skeleton (version gate, bounded concurrency, attempt budget with re-enqueue,
// shutdown requeue) but decodes the generic evidence job rather than the
// wiki-shaped crawl job. The duplication is intentional, matching the
// deferred-consolidation note in internal/crawljob: the two workers differ only
// in the job shape they decode, so a shared generic worker would thread both
// through type parameters for no behavior gain. Collapse them into one generic
// worker only if a third near-identical evidence consumer lands.
package evidencejob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Stats is the running outcome of a Worker drain: acknowledged deliveries and
// deliveries parked in the dead-letter queue. It is read after Run returns to
// report the drain to the operator, symmetrically to a producer run.
type Stats struct {
	Processed   int64
	ParkedToDLQ int64
}

// Action is what the consume loop must do with a delivery after Process decides
// the job's fate.
type Action int

const (
	// ActionAck drops the delivery: it was handled or is obsolete.
	ActionAck Action = iota
	// ActionRepublish re-enqueues the job (attempt incremented) then drops the original.
	ActionRepublish
	// ActionRequeue returns the delivery unhandled because shutdown cut work short.
	ActionRequeue
	// ActionReject dead-letters the delivery: a poison message or a job that
	// exhausted its retry budget is parked in the DLQ, never silently acked away.
	ActionReject
)

// Result is the outcome of processing one message.
type Result struct {
	Action            Action
	RepublishBody     []byte
	RepublishPriority uint8
}

// Embedder embeds chunk text for storage; the Voyage client with its retry and
// rate-limit decorators satisfies it.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// Store upserts a fully embedded chunk into the live corpus. The write is
// idempotent: a redelivered job rewrites the same row, keyed by the chunk's
// natural key (source, external_id, chunk_index).
type Store interface {
	UpsertEmbeddedChunk(ctx context.Context, chunk domain.EvidenceChunk) error
}

// Delivery is one job message awaiting acknowledgement, abstracting the broker.
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

// Enqueuer re-enqueues a job body at a priority for a bounded retry.
type Enqueuer interface {
	Enqueue(ctx context.Context, body []byte, priority uint8) error
}

// Config tunes a Worker. Concurrency caps parallel embeds per replica;
// MaxAttempts is the per-job delivery budget; KnownVersions is the set of queue
// schema versions this worker understands (empty disables the check).
type Config struct {
	Concurrency   int
	MaxAttempts   int
	KnownVersions []string
}

// Worker drains evidence jobs and upserts their embedded chunks into the live
// corpus.
type Worker struct {
	embedder      Embedder
	store         Store
	stream        Stream
	enqueuer      Enqueuer
	logger        *slog.Logger
	concurrency   int
	maxAttempts   int
	knownVersions map[string]struct{}

	// processed counts acknowledged deliveries and parked counts dead-lettered
	// ones, touched from the parallel handlers, so a run reports its drain outcome.
	processed atomic.Int64
	parked    atomic.Int64
}

// Stats reports the drain outcome accumulated so far: acknowledged and
// dead-lettered delivery counts. It is safe to call after Run returns.
func (w *Worker) Stats() Stats {
	return Stats{Processed: w.processed.Load(), ParkedToDLQ: w.parked.Load()}
}

// NewWorker builds a Worker, clamping concurrency and attempts to at least one
// and defaulting a nil logger.
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

// Run consumes the queue until ctx is canceled, processing up to Concurrency jobs
// in parallel. On shutdown an in-flight handler leaves its delivery unacked so the
// broker redelivers it; the idempotent upsert makes the re-embed safe.
func (w *Worker) Run(ctx context.Context) error {
	deliveries, err := w.stream.Consume(ctx)
	if err != nil {
		return fmt.Errorf("evidencejob: start consumer: %w", err)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, w.concurrency)
loop:
	for d := range deliveries {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
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

// handle processes one delivery and applies the broker action Process chose.
func (w *Worker) handle(ctx context.Context, d Delivery) {
	if !w.knowsVersion(d.Version()) {
		w.logger.ErrorContext(ctx, "dead-lettering evidence job with unknown queue version", slog.String("version", d.Version()))
		w.nack(ctx, d, false)
		return
	}
	res := w.Process(ctx, d.Body(), d.Priority())
	switch res.Action {
	case ActionRepublish:
		if err := w.enqueuer.Enqueue(ctx, res.RepublishBody, res.RepublishPriority); err != nil {
			if ctx.Err() != nil {
				w.logger.InfoContext(ctx, "re-enqueue interrupted by shutdown, requeuing original delivery", slog.Any("err", err))
				w.nack(ctx, d, true)
				return
			}
			// Re-enqueue failed for a non-shutdown reason: park the original in the
			// DLQ rather than requeue it forever with an unadvanced attempt.
			w.logger.ErrorContext(ctx, "re-enqueue failed, dead-lettering original delivery", slog.Any("err", err))
			w.nack(ctx, d, false)
			return
		}
		w.ack(ctx, d)
	case ActionRequeue:
		w.nack(ctx, d, true)
	case ActionReject:
		w.nack(ctx, d, false)
	default:
		w.ack(ctx, d)
	}
}

func (w *Worker) ack(ctx context.Context, d Delivery) {
	w.processed.Add(1)
	if err := d.Ack(); err != nil {
		w.logger.ErrorContext(ctx, "ack failed", slog.Any("err", err))
	}
}

func (w *Worker) nack(ctx context.Context, d Delivery, requeue bool) {
	// A requeue=false nack dead-letters the delivery to the DLQ; count those. A
	// requeue nack (shutdown or transient) is not a parked message.
	if !requeue {
		w.parked.Add(1)
	}
	if err := d.Nack(requeue); err != nil {
		w.logger.ErrorContext(ctx, "nack failed", slog.Any("err", err), slog.Bool("requeue", requeue))
	}
}

// Process embeds the job in body and upserts its chunk, returning the action the
// caller must take. It never returns an error: a malformed or invalid message and
// a persistent failure fold into ActionReject (after an ERROR log, so the message
// is parked in the DLQ, not lost), a transient failure into ActionRepublish, and a
// shutdown into ActionRequeue.
func (w *Worker) Process(ctx context.Context, body []byte, priority uint8) Result {
	var job connector.EvidenceJob
	if err := json.Unmarshal(body, &job); err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering malformed evidence job", slog.Any("err", err))
		return Result{Action: ActionReject}
	}
	if err := job.Validate(); err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering invalid evidence job", slog.Any("err", err))
		return Result{Action: ActionReject}
	}

	embeddings, err := w.embedder.EmbedDocuments(ctx, []string{job.Content})
	if err != nil {
		return w.afterFailure(ctx, job, priority, "embed", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != domain.EmbeddingDim {
		got := 0
		if len(embeddings) == 1 {
			got = len(embeddings[0])
		}
		w.logger.ErrorContext(ctx, "dead-lettering evidence job with unexpected provider response",
			slog.String("source", job.Source), slog.String("external_id", job.ExternalID), slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("vectors", len(embeddings)), slog.Int("dims", got), slog.Int("want_dims", domain.EmbeddingDim))
		return Result{Action: ActionReject}
	}

	chunk := job.Chunk()
	chunk.Embedding = embeddings[0]
	if err := w.store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		return w.afterFailure(ctx, job, priority, "upsert", err)
	}
	return Result{Action: ActionAck}
}

// afterFailure decides what to do with a job whose embed or upsert failed. A
// canceled context means shutdown: requeue without counting the attempt.
// Otherwise the attempt counts: dead-letter with an ERROR log when the budget is
// spent, else re-enqueue with the attempt incremented at the same priority.
func (w *Worker) afterFailure(ctx context.Context, job connector.EvidenceJob, priority uint8, stage string, cause error) Result {
	if ctx.Err() != nil {
		w.logger.InfoContext(ctx, "evidence job interrupted by shutdown, requeuing",
			slog.String("stage", stage), slog.String("source", job.Source),
			slog.String("external_id", job.ExternalID), slog.Int("chunk_index", job.ChunkIndex))
		return Result{Action: ActionRequeue}
	}
	if job.Attempt >= w.maxAttempts-1 {
		w.logger.ErrorContext(ctx, "dead-lettering evidence job after exhausting retries",
			slog.String("stage", stage), slog.String("source", job.Source),
			slog.String("external_id", job.ExternalID), slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("attempt", job.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
		return Result{Action: ActionReject}
	}
	retry := job
	retry.Attempt = job.Attempt + 1
	encoded, err := json.Marshal(retry)
	if err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering evidence job that cannot be re-encoded for retry",
			slog.String("source", job.Source), slog.String("external_id", job.ExternalID),
			slog.Int("chunk_index", job.ChunkIndex), slog.Any("err", err))
		return Result{Action: ActionReject}
	}
	w.logger.WarnContext(ctx, "evidence job failed, re-enqueuing for retry",
		slog.String("stage", stage), slog.String("source", job.Source),
		slog.String("external_id", job.ExternalID), slog.Int("chunk_index", job.ChunkIndex),
		slog.Int("next_attempt", retry.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
	return Result{Action: ActionRepublish, RepublishBody: encoded, RepublishPriority: priority}
}
