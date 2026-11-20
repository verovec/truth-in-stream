// Package scrutinsjob is the scrutins-archive consumer logic: it drains
// self-contained scrutin jobs from the scrutins queue, parses each scrutin's raw
// AN open-data JSON into per-deputy voting records, and upserts them into the
// voting store. It is transport-free - it depends on its own small
// Delivery/Stream/Enqueuer interfaces and the voting-record Store, never on a
// concrete broker or any HTTP type - so the worker is unit-testable and the
// broker is swappable behind the cmd-layer adapters.
//
// It mirrors internal/factcheckjob's broker/retry skeleton (version gate,
// bounded concurrency, attempt budget with re-enqueue, shutdown requeue) but
// writes voting records parsed from a self-contained scrutin payload rather than
// an embedded curated claim. The duplication is intentional, matching the
// deferred-consolidation note in internal/factcheckjob: the two workers differ
// in their domain write type and validation, so a shared generic worker would
// thread both through type parameters for no behavior gain. Collapse them into
// one generic worker only if a third near-identical consumer lands.
package scrutinsjob

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/votingrecord"
)

// Stats is the running outcome of a Worker drain: acknowledged deliveries and
// deliveries parked in the dead-letter queue. It is read after Run returns to
// report the drain to the operator, symmetrically to a producer run.
type Stats struct {
	Processed   int64
	ParkedToDLQ int64
}

// ScrutinJob is one unit of scrutins-ingest work: the raw AN open-data JSON for a
// single scrutin ({"scrutin": {...}}), exactly as it appears in the archive. The
// whole scrutin payload travels in the body so the worker performs no archive
// read before writing. ID is the scrutin uid, carried alongside the payload for
// logging and so a malformed body can still be named in an error. Attempt is the
// delivery attempt so far; the producer leaves it zero and the worker increments
// it on a transient-failure re-enqueue so a job that keeps failing is eventually
// dropped.
type ScrutinJob struct {
	ID string `json:"id"`
	// Chamber selects the parser. Empty (the default, backward-compatible with the
	// Assemblee scrutins archive) means the Assemblee open-data JSON, parsed by
	// votingrecord.ParseScrutin. "senat" means the self-contained Senat payload the
	// parliament producer publishes, parsed by votingrecord.ParseSenatScrutin. Either
	// way the worker writes voting_records with the right chamber.
	Chamber string          `json:"chamber,omitempty"`
	Scrutin json.RawMessage `json:"scrutin"`
	Attempt int             `json:"attempt,omitzero"`
}

// validate rejects a job that can never succeed, so the worker drops it instead
// of looping forever. The scrutin payload's own shape is validated by the
// parser; this only guards the envelope fields.
func (j ScrutinJob) validate() error {
	switch {
	case j.ID == "":
		return fmt.Errorf("scrutin job has empty id")
	case len(j.Scrutin) == 0:
		return fmt.Errorf("scrutin %q has empty payload", j.ID)
	case j.Attempt < 0:
		return fmt.Errorf("scrutin %q has a negative attempt %d", j.ID, j.Attempt)
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

// Store upserts a scrutin's parsed voting records into the voting store in one
// atomic apply, so a concurrent reader never sees a partial vote set while a
// scrutin is being written (or rewritten). The write is idempotent: a redelivered
// job (same scrutin) rewrites the same rows, keyed by (person, scrutin), so
// re-running the pipeline is safe.
type Store interface {
	UpsertVotingRecords(ctx context.Context, records []domain.VotingRecord) error
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

// Config tunes a Worker. Concurrency caps parallel scrutins processed per
// replica; MaxAttempts is the per-job delivery budget; KnownVersions is the set
// of queue schema versions this worker understands (empty disables the check).
type Config struct {
	Concurrency   int
	MaxAttempts   int
	KnownVersions []string
}

// Worker drains scrutin jobs and upserts their parsed voting records into the
// voting store.
type Worker struct {
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
func NewWorker(store Store, stream Stream, enqueuer Enqueuer, logger *slog.Logger, cfg Config) *Worker {
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
// broker redelivers it; the idempotent upsert makes the re-parse safe.
func (w *Worker) Run(ctx context.Context) error {
	deliveries, err := w.stream.Consume(ctx)
	if err != nil {
		return fmt.Errorf("scrutinsjob: start consumer: %w", err)
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
		w.logger.ErrorContext(ctx, "dead-lettering scrutin job with unknown queue version", slog.String("version", d.Version()))
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
	// A requeue=false nack dead-letters the delivery to the DLQ; count those so the
	// drain reports its parked total. A requeue nack (shutdown or transient) is not
	// a parked message, so it is not counted.
	if !requeue {
		w.parked.Add(1)
	}
	if err := d.Nack(requeue); err != nil {
		w.logger.ErrorContext(ctx, "nack failed", slog.Any("err", err), slog.Bool("requeue", requeue))
	}
}

// Process parses the scrutin in body and upserts its voting records, returning
// the action the caller must take. It never returns an error: a malformed or
// invalid message and a persistent failure fold into ActionReject (after an ERROR
// log, so the message is parked in the DLQ, not lost), a transient store failure
// into ActionRepublish, and a shutdown into ActionRequeue. The scrutin payload is
// re-wrapped into the {"scrutin": {...}} envelope the parser expects, since
// ScrutinJob.Scrutin carries the inner object alone.
func (w *Worker) Process(ctx context.Context, body []byte, priority uint8) Result {
	var job ScrutinJob
	if err := json.Unmarshal(body, &job); err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering malformed scrutin job", slog.Any("err", err))
		return Result{Action: ActionReject}
	}
	if err := job.validate(); err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering invalid scrutin job", slog.Any("err", err))
		return Result{Action: ActionReject}
	}

	records, err := parseRecords(job)
	if err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering unparseable scrutin job",
			slog.String("id", job.ID), slog.String("chamber", job.Chamber), slog.Any("err", err))
		return Result{Action: ActionReject}
	}

	// A shutdown between the parse and the write is a clean requeue point; the write
	// itself is atomic, so it either fully lands or fully rolls back.
	if ctx.Err() != nil {
		return w.afterFailure(ctx, job, priority, "upsert", ctx.Err())
	}
	if err := w.store.UpsertVotingRecords(ctx, records); err != nil {
		return w.afterFailure(ctx, job, priority, "upsert", err)
	}
	return Result{Action: ActionAck}
}

// parseRecords parses a scrutin job into voting records, dispatching on the
// chamber: an empty chamber is the Assemblee open-data JSON (re-wrapped in its
// {"scrutin": {...}} envelope for votingrecord.ParseScrutin), and "senat" is the
// self-contained Senat payload votingrecord.ParseSenatScrutin reads directly. A
// chamber the worker does not know is an error, so a mis-stamped job is
// dead-lettered rather than silently written under the wrong parser.
func parseRecords(job ScrutinJob) ([]domain.VotingRecord, error) {
	switch job.Chamber {
	case "", string(domain.ChamberAssemblee):
		return votingrecord.ParseScrutin(wrapScrutin(job.Scrutin))
	case string(domain.ChamberSenat):
		return votingrecord.ParseSenatScrutin(job.Scrutin)
	default:
		return nil, fmt.Errorf("scrutinsjob: unknown chamber %q", job.Chamber)
	}
}

// wrapScrutin restores the {"scrutin": {...}} envelope votingrecord.ParseScrutin
// expects from the bare inner object the job carries, so the producer transports
// only the scrutin object while the parser stays unchanged.
func wrapScrutin(inner json.RawMessage) []byte {
	wrapped := make([]byte, 0, len(inner)+len(`{"scrutin":}`))
	wrapped = append(wrapped, `{"scrutin":`...)
	wrapped = append(wrapped, inner...)
	wrapped = append(wrapped, '}')
	return wrapped
}

// afterFailure decides what to do with a job whose upsert failed. A canceled
// context means shutdown: requeue without counting the attempt. Otherwise the
// attempt counts: dead-letter with an ERROR log when the budget is spent, else
// re-enqueue with the attempt incremented at the same priority.
func (w *Worker) afterFailure(ctx context.Context, job ScrutinJob, priority uint8, stage string, cause error) Result {
	if ctx.Err() != nil {
		w.logger.InfoContext(ctx, "scrutin job interrupted by shutdown, requeuing",
			slog.String("stage", stage), slog.String("id", job.ID))
		return Result{Action: ActionRequeue}
	}
	// Attempt is zero-indexed, so the last allowed attempt is maxAttempts-1; at or
	// past it the budget is spent and the job is dead-lettered rather than re-enqueued.
	if job.Attempt >= w.maxAttempts-1 {
		w.logger.ErrorContext(ctx, "dead-lettering scrutin job after exhausting retries",
			slog.String("stage", stage), slog.String("id", job.ID),
			slog.Int("attempt", job.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
		return Result{Action: ActionReject}
	}
	retry := job
	retry.Attempt = job.Attempt + 1
	encoded, err := json.Marshal(retry)
	if err != nil {
		w.logger.ErrorContext(ctx, "dead-lettering scrutin job that cannot be re-encoded for retry",
			slog.String("id", job.ID), slog.Any("err", err))
		return Result{Action: ActionReject}
	}
	w.logger.WarnContext(ctx, "scrutin job failed, re-enqueuing for retry",
		slog.String("stage", stage), slog.String("id", job.ID),
		slog.Int("next_attempt", retry.Attempt), slog.Int("max_attempts", w.maxAttempts), slog.Any("err", cause))
	return Result{Action: ActionRepublish, RepublishBody: encoded, RepublishPriority: priority}
}
