// Package factcheckjob is the fact-check-archive consumer logic: it drains
// self-contained curated-claim jobs from the fact-check queue, embeds each
// claim's text through the Voyage embedder, and upserts the curated claim record
// (text + verdict + flags + source + vector) straight into the political claim
// DB. It is transport-free - it depends on its own small Stream/Delivery/Store/
// Enqueuer interfaces, never on a concrete broker or any HTTP type - so the
// worker is unit-testable and the broker is swappable behind the cmd-layer
// adapters. It mirrors internal/crawljob, but writes a curated political claim
// (whose verdict and source travel in the self-contained message) rather than a
// wiki chunk. The broker/retry skeleton is intentionally duplicated rather than
// generalized: the two workers differ in their domain write type and validation,
// so a shared generic worker would thread both through type parameters for no
// behavior gain. Collapse them into one generic worker only if a third
// near-identical consumer lands (the deferred-consolidation trigger the
// internal/llm adapters follow).
package factcheckjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ClaimJob is one unit of fact-check-ingest work: a fully self-contained curated
// claim. Every field needed to write a political_claims row travels in the body,
// so the worker performs no database read before writing. ID is the stable,
// content-independent key (the fact-check article URL) so a re-published job
// rewrites the same row; CheckedAt is an RFC3339 string (empty = no date known)
// kept as text in the message so the producer needs no time parsing. Attempt is
// the delivery attempt so far; the producer leaves it zero and the worker
// increments it on a transient-failure re-enqueue so a job that keeps failing is
// eventually dropped.
type ClaimJob struct {
	ID             string   `json:"id"`
	Text           string   `json:"text"`
	LiteralVerdict string   `json:"literal_verdict"`
	Flags          []string `json:"flags,omitempty"`
	SourceName     string   `json:"source_name"`
	SourceURL      string   `json:"source_url"`
	QuotedSpan     string   `json:"quoted_span,omitempty"`
	Outlet         string   `json:"outlet"`
	CheckedAt      string   `json:"checked_at,omitempty"`
	Attempt        int      `json:"attempt,omitzero"`
}

// checkedAt parses the optional RFC3339 publication timestamp. An empty string is
// a valid "no date recorded" and yields the zero time, which the store maps to
// SQL NULL; any other unparseable value is a hard error so a malformed job is
// dropped rather than silently stored with a wrong date.
func (j ClaimJob) checkedAt() (time.Time, error) {
	if j.CheckedAt == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, j.CheckedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse checked-at %q: %w", j.CheckedAt, err)
	}
	return t, nil
}

// validate rejects a job that can never succeed, so the worker drops it instead
// of embedding nonsense or looping forever. It mirrors the political_claims
// column constraints (verdict and flags against the domain enums) so bad data is
// rejected before it reaches the store.
func (j ClaimJob) validate() error {
	switch {
	case j.ID == "":
		return fmt.Errorf("claim job has empty id")
	case j.Text == "":
		return fmt.Errorf("claim %q has empty text", j.ID)
	case !domain.LiteralVerdict(j.LiteralVerdict).Valid():
		return fmt.Errorf("claim %q has invalid literal verdict %q", j.ID, j.LiteralVerdict)
	case j.SourceURL == "":
		return fmt.Errorf("claim %q has empty source url", j.ID)
	case j.Outlet == "":
		return fmt.Errorf("claim %q has empty outlet", j.ID)
	case j.Attempt < 0:
		return fmt.Errorf("claim %q has a negative attempt %d", j.ID, j.Attempt)
	}
	for _, f := range j.Flags {
		if !domain.ManipulationFlag(f).Valid() {
			return fmt.Errorf("claim %q has invalid manipulation flag %q", j.ID, f)
		}
	}
	if _, err := j.checkedAt(); err != nil {
		return fmt.Errorf("claim %q: %w", j.ID, err)
	}
	return nil
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

// Embedder embeds claim text for storage; the Voyage client with its retry and
// rate-limit decorators satisfies it.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// Store upserts a fully embedded curated claim into the political claim DB. The
// write is idempotent: a redelivered job (same ID) rewrites the same row.
type Store interface {
	UpsertPoliticalClaim(ctx context.Context, claim domain.PoliticalClaim) error
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

// Worker drains fact-check claim jobs and upserts their embedded curated claims
// into the political claim DB.
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
		return fmt.Errorf("factcheckjob: start consumer: %w", err)
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
		w.logger.ErrorContext(ctx, "dead-lettering fact-check job with unknown queue version", slog.String("version", d.Version()))
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
	if err := d.Ack(); err != nil {
		w.logger.ErrorContext(ctx, "ack failed", slog.Any("err", err))
	}
}

func (w *Worker) nack(ctx context.Context, d Delivery, requeue bool) {
	if err := d.Nack(requeue); err != nil {
		w.logger.ErrorContext(ctx, "nack failed", slog.Any("err", err), slog.Bool("requeue", requeue))
	}
}

// Process embeds the job in body and upserts its curated claim, returning the
// action the caller must take. It never returns an error: a malformed or invalid
// message and a persistent failure fold into ActionReject (after an ERROR log, so
// the message is parked in the DLQ, not lost), a transient failure into
// ActionRepublish, and a shutdown into ActionRequeue.
func (w *Worker) Process(ctx context.Context, body []byte, priority uint8) Result {
	var job ClaimJob
	if err := json.Unmarshal(body, &job); err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering malformed fact-check job", slog.Any("err", err))
		return Result{Action: ActionReject}
	}
	if err := job.validate(); err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering invalid fact-check job", slog.Any("err", err))
		return Result{Action: ActionReject}
	}

	embeddings, err := w.embedder.EmbedDocuments(ctx, []string{job.Text})
	if err != nil {
		return w.afterFailure(ctx, job, priority, "embed", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != domain.EmbeddingDim {
		got := 0
		if len(embeddings) == 1 {
			got = len(embeddings[0])
		}
		w.logger.ErrorContext(ctx, "dead-lettering fact-check job with unexpected provider response",
			slog.String("id", job.ID), slog.Int("vectors", len(embeddings)),
			slog.Int("dims", got), slog.Int("want_dims", domain.EmbeddingDim))
		return Result{Action: ActionReject}
	}

	// validate already proved checkedAt parses, so the error path is unreachable.
	checkedAt, _ := job.checkedAt()
	flags := make([]domain.ManipulationFlag, 0, len(job.Flags))
	for _, f := range job.Flags {
		flags = append(flags, domain.ManipulationFlag(f))
	}
	claim := domain.PoliticalClaim{
		ID:             job.ID,
		Text:           job.Text,
		LiteralVerdict: domain.LiteralVerdict(job.LiteralVerdict),
		Flags:          flags,
		SourceName:     job.SourceName,
		SourceURL:      job.SourceURL,
		QuotedSpan:     job.QuotedSpan,
		Outlet:         job.Outlet,
		CheckedAt:      checkedAt,
		Embedding:      embeddings[0],
	}
	if err := w.store.UpsertPoliticalClaim(ctx, claim); err != nil {
		return w.afterFailure(ctx, job, priority, "upsert", err)
	}
	return Result{Action: ActionAck}
}

// afterFailure decides what to do with a job whose embed or upsert failed. A
// canceled context means shutdown: requeue without counting the attempt.
// Otherwise the attempt counts: dead-letter with an ERROR log when the budget is
// spent, else re-enqueue with the attempt incremented at the same priority.
func (w *Worker) afterFailure(ctx context.Context, job ClaimJob, priority uint8, stage string, cause error) Result {
	if ctx.Err() != nil {
		w.logger.InfoContext(ctx, "fact-check job interrupted by shutdown, requeuing",
			slog.String("stage", stage), slog.String("id", job.ID))
		return Result{Action: ActionRequeue}
	}
	if job.Attempt >= w.maxAttempts-1 {
		w.logger.ErrorContext(ctx, "dead-lettering fact-check job after exhausting retries",
			slog.String("stage", stage), slog.String("id", job.ID),
			slog.Int("attempt", job.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
		return Result{Action: ActionReject}
	}
	retry := job
	retry.Attempt = job.Attempt + 1
	encoded, err := json.Marshal(retry)
	if err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering fact-check job that cannot be re-encoded for retry",
			slog.String("id", job.ID), slog.Any("err", err))
		return Result{Action: ActionReject}
	}
	w.logger.WarnContext(ctx, "fact-check job failed, re-enqueuing for retry",
		slog.String("stage", stage), slog.String("id", job.ID),
		slog.Int("next_attempt", retry.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
	return Result{Action: ActionRepublish, RepublishBody: encoded, RepublishPriority: priority}
}
