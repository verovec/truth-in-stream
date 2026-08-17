// Package embedjob is the embedding-worker consumer logic: it drains embedding
// jobs from the priority queue, embeds chunk text through the Voyage embedder in
// batches, and writes the vectors into the live corpus (or staging, for an
// atomic rebuild). It is transport-free - it depends on its own small
// Stream/Delivery/Enqueuer interfaces, never on a concrete broker or any HTTP
// type - so the worker is unit-testable and the broker is swappable behind the
// cmd-layer adapters.
//
// Throughput comes from batching: the worker buffers up to a batch size (or a
// short max-wait window, so a quiet queue still drains) and embeds the whole
// buffer in one provider call, paying the round-trip once per batch instead of
// once per chunk. Each NULL->vector write makes its chunk searchable
// immediately, so the corpus grows monotonically while the fleet embeds rather
// than waiting for a wholesale swap.
package embedjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
)

// Voyage's documented per-request ceilings for voyage-4-large
// (docs.voyageai.com, verified 2026-07): at most maxInputsPerRequest inputs and
// maxTokensPerRequest tokens across the whole request. A request over either
// returns HTTP 400 and fails the entire call, so the worker keeps every provider
// call under both ceilings and splits an over-budget batch rather than thrashing.
const (
	maxInputsPerRequest = 1000
	maxTokensPerRequest = 120000
)

// defaultBatchTokens is the token budget the worker packs each provider call to.
// It sits below the hard maxTokensPerRequest ceiling because the token count is
// estimated from character counts (see estimateTokens), which under-counts a
// token-dense input (many short words, or CJK where one character is often one
// token); the 80% headroom absorbs that error so a batch that estimates under
// budget almost never trips the provider's hard limit. When it does anyway, the
// recursive split recovers.
const defaultBatchTokens = maxTokensPerRequest * 8 / 10

// charsPerToken is Voyage's documented average characters per token
// (docs.voyageai.com), used to estimate a batch's token count cheaply without
// calling the tokenizer. estimateTokens divides byte length by it, so a
// multibyte input (whose bytes exceed its characters) is over-counted, keeping
// the estimate conservative.
const charsPerToken = 5

// estimateTokens cheaply approximates a text's token count from its byte length,
// rounding up so even a short non-empty text counts as at least one token.
func estimateTokens(text string) int {
	return len(text)/charsPerToken + 1
}

// Stats is the running outcome of a Worker drain: how many deliveries it
// acknowledged and how many it parked in the dead-letter queue. It is read after
// Run returns to report the drain to the operator.
type Stats struct {
	Processed   int64
	ParkedToDLQ int64
}

// Job is one unit of embedding work: the chunk to embed, identified by its
// corpus position, the text to embed, and the delivery attempt so far. The
// producer sets Attempt to zero; the worker increments it when it re-enqueues a
// transient failure, so a job that keeps failing is eventually dropped rather
// than looping forever.
type Job struct {
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
	ChunkIndex int    `json:"chunk_index"`
	Content    string `json:"content"`
	Attempt    int    `json:"attempt,omitzero"`
	// Staging routes the embedding write to the staging corpus instead of the
	// live one. The bulk-into-live default (false) writes straight into
	// evidence_chunks, so the chunk is searchable the moment its vector lands; an
	// atomic-rebuild producer sets it true so the fleet fills staging for a later
	// wholesale swap. It is omitted on the wire when false, so a live job's
	// encoding is unchanged.
	Staging bool `json:"staging,omitempty"`
}

// validate rejects a job that can never succeed, so the worker drops it instead
// of embedding nonsense or looping. A chunk is identified by its (source,
// external_id, chunk_index): source and external_id must be non-empty and the
// index non-negative; empty content has nothing to embed; a negative attempt is
// a corrupt or overflowed counter that must not be retried.
func (j Job) validate() error {
	switch {
	case j.Source == "":
		return fmt.Errorf("source must not be empty")
	case j.ExternalID == "":
		return fmt.Errorf("external id must not be empty")
	case j.ChunkIndex < 0:
		return fmt.Errorf("chunk index %d must not be negative", j.ChunkIndex)
	case j.Content == "":
		return fmt.Errorf("%s/%s chunk %d has empty content", j.Source, j.ExternalID, j.ChunkIndex)
	case j.Attempt < 0:
		return fmt.Errorf("%s/%s chunk %d has a negative attempt %d", j.Source, j.ExternalID, j.ChunkIndex, j.Attempt)
	default:
		return nil
	}
}

// Action is what the consume loop must do with a delivery after Process decides
// the job's fate.
type Action int

const (
	// ActionAck drops the delivery: the job was handled or was obsolete.
	ActionAck Action = iota
	// ActionRepublish re-enqueues the job (with its attempt incremented) for a
	// bounded retry, then drops the original.
	ActionRepublish
	// ActionRequeue returns the delivery to the broker unhandled because a
	// shutdown cut the work short, so it is redelivered without burning an attempt.
	ActionRequeue
	// ActionReject dead-letters the delivery: a poison message or one past its
	// retry budget is parked in the DLQ, never silently acked away.
	ActionReject
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

// Store writes chunks' embeddings into the corpus, keyed on identity and
// content. matched is how many rows the write actually updated; a row that does
// not match (the chunk left the corpus, its content changed under a re-ingest,
// or - for staging - the table was already swapped live) is left untouched, so
// the worker can drop an obsolete job rather than retry it. Writes are
// idempotent: a redelivered batch rewrites the same vectors safely.
type Store interface {
	SetLiveChunkEmbeddings(ctx context.Context, chunks []domain.EvidenceChunk) (matched int, err error)
	SetStagingChunkEmbeddings(ctx context.Context, chunks []domain.EvidenceChunk) (matched int, err error)
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

// defaultBatchWait bounds how long a partial batch waits for more deliveries
// before it is embedded anyway, so a quiet queue still drains promptly.
const defaultBatchWait = 200 * time.Millisecond

// Config tunes a Worker. Concurrency caps the batches one replica embeds in
// parallel (the fleet scales by replica count); BatchSize is the most chunks
// embedded in one provider call and BatchWait bounds how long a partial batch
// waits for more before it is sent anyway. MaxAttempts is the total delivery
// budget for a job before a persistent failure is dropped with a log.
// KnownVersions is the set of queue schema versions this worker understands: a
// delivery stamped with any other version is dropped rather than mis-processed.
// An empty KnownVersions disables the check (every version is accepted), which
// keeps a worker that does not configure versions working unchanged.
// MaxBatchTokens caps the estimated token count of one provider call; a batch
// whose chunks would exceed it is split before the call so the request stays
// under Voyage's per-request token ceiling. Zero selects defaultBatchTokens.
type Config struct {
	Concurrency    int
	BatchSize      int
	BatchWait      time.Duration
	MaxAttempts    int
	MaxBatchTokens int
	KnownVersions  []string
}

// Worker drains embedding jobs and writes their vectors into the corpus.
type Worker struct {
	embedder      Embedder
	store         Store
	stream        Stream
	enqueuer      Enqueuer
	logger        *slog.Logger
	concurrency   int
	batchSize     int
	batchWait     time.Duration
	maxAttempts   int
	maxInputs     int
	maxTokens     int
	knownVersions map[string]struct{}

	// processed counts acknowledged deliveries and parked counts dead-lettered
	// ones, so a run reports its drain outcome. Both are touched from the parallel
	// batch handlers, so they are atomic.
	processed atomic.Int64
	parked    atomic.Int64
}

// NewWorker builds a Worker. Concurrency, BatchSize, and MaxAttempts below one
// are clamped to one (a worker must run at least one batch of at least one job
// and try it at least once), a non-positive BatchWait takes the default window,
// and a nil logger falls back to the default so the worker always has one.
func NewWorker(embedder Embedder, store Store, stream Stream, enqueuer Enqueuer, logger *slog.Logger, cfg Config) *Worker {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.BatchSize < 1 {
		cfg.BatchSize = 1
	}
	if cfg.BatchWait <= 0 {
		cfg.BatchWait = defaultBatchWait
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if cfg.MaxBatchTokens < 1 {
		cfg.MaxBatchTokens = defaultBatchTokens
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
		batchSize:     cfg.BatchSize,
		batchWait:     cfg.BatchWait,
		maxAttempts:   cfg.MaxAttempts,
		maxInputs:     maxInputsPerRequest,
		maxTokens:     cfg.MaxBatchTokens,
		knownVersions: known,
	}
}

// Stats reports the drain outcome accumulated so far: acknowledged and
// dead-lettered delivery counts. It is safe to call after Run returns.
func (w *Worker) Stats() Stats {
	return Stats{Processed: w.processed.Load(), ParkedToDLQ: w.parked.Load()}
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

// stopTimer stops t and drains a pending fire so a later Reset starts clean. It
// is only ever called from Run's single loop goroutine, so the non-blocking
// drain is race-free.
func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// Run consumes the queue until ctx is canceled, batching deliveries and
// processing up to Concurrency batches in parallel. A batch is dispatched once it
// reaches BatchSize or its BatchWait window elapses, whichever comes first, so a
// busy queue fills whole batches and a quiet one still drains. It returns once it
// stops admitting work - the delivery stream closed or ctx was canceled - and
// every in-flight batch has finished. On shutdown, collected-but-undispatched
// deliveries are nacked for requeue and an in-flight batch sees the canceled
// context and requeues its own deliveries, so a scale-down loses nothing (at the
// cost of re-embedding the interrupted few, which the idempotent write makes
// safe).
func (w *Worker) Run(ctx context.Context) error {
	deliveries, err := w.stream.Consume(ctx)
	if err != nil {
		return fmt.Errorf("embedjob: start consumer: %w", err)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, w.concurrency)
	var batch []Delivery
	timer := time.NewTimer(w.batchWait)
	stopTimer(timer)
	timerRunning := false

	// flush dispatches the current batch to a bounded handler goroutine. It blocks
	// for a free slot (backpressure), and returns false if ctx is canceled while
	// waiting for one, leaving the batch intact for the caller to requeue.
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		b := batch
		batch = nil
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			w.processBatch(ctx, b)
		}()
		return true
	}

	// add appends a delivery, dispatching as soon as the batch is full and
	// otherwise (re)arming the max-wait window. It returns false only when a
	// size-triggered flush is cut short by shutdown.
	add := func(d Delivery) bool {
		batch = append(batch, d)
		if len(batch) >= w.batchSize {
			if timerRunning {
				stopTimer(timer)
				timerRunning = false
			}
			return flush()
		}
		if !timerRunning {
			timer.Reset(w.batchWait)
			timerRunning = true
		}
		return true
	}

	// Termination is driven by the stream closing, which the Stream contract
	// guarantees on ctx cancellation - so a canceled run is not abandoned mid
	// rendezvous: every delivery already in flight is still received and then
	// requeued (by the in-flight batch, on its canceled context) rather than
	// dropped. ctx is consulted only for backpressure (flush's slot wait) so a
	// scale-down does not block forever for a free slot.
loop:
	for {
		// Prefer draining a ready delivery over firing the wait timer, so a busy
		// queue fills whole batches instead of timing out on partial ones.
		select {
		case d, ok := <-deliveries:
			if !ok {
				break loop
			}
			if !add(d) {
				w.requeueAll(ctx, batch)
				batch = nil
				break loop
			}
			continue
		default:
		}
		select {
		case d, ok := <-deliveries:
			if !ok {
				break loop
			}
			if !add(d) {
				w.requeueAll(ctx, batch)
				batch = nil
				break loop
			}
		case <-timer.C:
			timerRunning = false
			if !flush() {
				w.requeueAll(ctx, batch)
				batch = nil
				break loop
			}
		}
	}

	// Stream closed cleanly: embed the tail. On shutdown (or if the tail flush
	// cannot claim a slot before ctx is done) requeue it instead, so a scale-down
	// redelivers promptly rather than dropping the batch unacknowledged.
	if ctx.Err() != nil || !flush() {
		w.requeueAll(ctx, batch)
	}
	wg.Wait()
	return nil
}

// pending pairs a delivery with its decoded job, so a batch carries both the
// acknowledgement handle and the work.
type pending struct {
	d   Delivery
	job Job
}

// processBatch embeds a whole batch in one provider call and writes the vectors,
// then acknowledges each delivery. Messages that can never succeed (unknown
// version, malformed, invalid, or a bad provider shape) are dropped individually
// so one poison message cannot sink the batch. A batch-level embed failure falls
// back to embedding each delivery on its own, isolating a single bad input; a
// write failure re-enqueues the batch's jobs for a bounded retry; and a shutdown
// requeues them untouched.
func (w *Worker) processBatch(ctx context.Context, deliveries []Delivery) {
	items := make([]pending, 0, len(deliveries))
	for _, d := range deliveries {
		if !w.knowsVersion(d.Version()) {
			w.logger.ErrorContext(ctx, "dead-lettering embedding job with unknown queue version",
				slog.String("version", d.Version()))
			w.reject(ctx, d)
			continue
		}
		job, ok := w.decode(ctx, d.Body())
		if !ok {
			w.reject(ctx, d)
			continue
		}
		items = append(items, pending{d: d, job: job})
	}
	if len(items) == 0 {
		return
	}

	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.job.Content
	}

	vecs, errs := w.embedAligned(ctx, texts)
	if ctx.Err() != nil {
		w.requeueItems(ctx, items)
		return
	}

	good := make([]pending, 0, len(items))
	var live, staging []domain.EvidenceChunk
	for i, it := range items {
		if err := errs[i]; err != nil {
			// A single input the provider rejected even on its own (a genuinely
			// oversized or malformed chunk): retry it a bounded number of times, then
			// dead-letter it. The whole-batch call was already split down to this one
			// input, so the rest of the batch is unaffected - no per-chunk thrash.
			w.applyResult(ctx, it.d, w.afterFailure(ctx, it.job, it.d.Priority(), "embed", err))
			continue
		}
		vec := vecs[i]
		if len(vec) != domain.EmbeddingDim {
			// A wrong shape is the provider breaking its contract, not a transient
			// fault: re-embedding the same content would reproduce it, so drop rather
			// than loop, and never write a malformed vector into the corpus.
			w.logger.ErrorContext(ctx, "dead-lettering embedding job with unexpected provider response",
				slog.String("source", it.job.Source), slog.String("external_id", it.job.ExternalID),
				slog.Int("chunk_index", it.job.ChunkIndex),
				slog.Int("dims", len(vec)),
				slog.Int("want_dims", domain.EmbeddingDim))
			w.reject(ctx, it.d)
			continue
		}
		good = append(good, it)
		chunk := domain.EvidenceChunk{Source: it.job.Source, ExternalID: it.job.ExternalID, ChunkIndex: it.job.ChunkIndex, Content: it.job.Content, Embedding: vec}
		if it.job.Staging {
			staging = append(staging, chunk)
		} else {
			live = append(live, chunk)
		}
	}
	if len(good) == 0 {
		return
	}

	matched, err := w.writeChunks(ctx, live, staging)
	if err != nil {
		if ctx.Err() != nil {
			w.requeueItems(ctx, good)
			return
		}
		w.logger.WarnContext(ctx, "batch write failed, re-enqueuing its jobs",
			slog.Int("batch", len(good)), slog.Any("err", err))
		for _, g := range good {
			w.applyResult(ctx, g.d, w.afterFailure(ctx, g.job, g.d.Priority(), "write", err))
		}
		return
	}
	// A matched count below the batch size means some chunks were obsolete (gone,
	// or content changed under a re-ingest so the content guard skipped them).
	// They are dropped, like a single obsolete job, but logged so a stalling
	// coverage figure is explainable rather than a silent drop.
	if matched < len(good) {
		w.logger.InfoContext(ctx, "dropped obsolete embedding jobs (chunk gone or content changed)",
			slog.Int("dropped", len(good)-matched),
			slog.Int("batch", len(good)))
	}
	for _, g := range good {
		w.ack(ctx, g.d)
	}
}

// embedAligned embeds texts and returns, for each index, its vector (nil on
// failure) and the error that stopped it (nil on success). It keeps every
// provider call under Voyage's input-count and token-budget ceilings by
// splitting an over-budget group before the call, and recovers from a size-class
// 400 the character-based estimate under-counted by halving the group and
// retrying - so an oversized batch embeds via O(log n) split calls, never a
// per-chunk sweep (the 128x amplification the token-blind cap used to fall into).
// A non-size failure (auth, an exhausted 429, a 5xx, a network fault) is not
// split: it is recorded once for the whole group, which the caller retries or
// dead-letters, so a persistent fault never fans out into a binary-split storm of
// provider calls.
func (w *Worker) embedAligned(ctx context.Context, texts []string) ([][]float32, []error) {
	vecs := make([][]float32, len(texts))
	errs := make([]error, len(texts))
	w.embedGroup(ctx, texts, 0, vecs, errs)
	return vecs, errs
}

// embedGroup embeds texts[off:off+len(texts)] into vecs/errs. It splits an
// over-budget group before the call, and on a call failure splits only a
// size-class rejection (see isSizeError); any other error is recorded for the
// whole group so the caller handles it without amplifying provider load.
func (w *Worker) embedGroup(ctx context.Context, texts []string, off int, vecs [][]float32, errs []error) {
	if len(texts) == 0 {
		return
	}
	// Proactively split an over-budget group so a request the provider would reject
	// with a size-class 400 is never sent; repeated halving lands each group under
	// both the input-count and token ceilings.
	if len(texts) > 1 && w.overBudget(texts) {
		w.splitGroup(ctx, texts, off, vecs, errs)
		return
	}
	embeddings, err := w.embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		if ctx.Err() != nil {
			for i := range texts {
				errs[off+i] = err
			}
			return
		}
		// The error surfaced after the embedder's own retry decorator gave up, so it
		// is persistent. Split only a size-class 400 the token estimate under-counted:
		// halving lands the group under the ceiling in O(log n) calls. Any other
		// persistent error (auth, an exhausted 429, a 5xx, a network outage) is not a
		// size problem, so splitting it would re-run the same failure across the whole
		// ~2n-1 split tree (and re-drive the retry ladder against an already-failing
		// provider); record it once for the group and let afterFailure and the DLQ
		// handle it on redelivery instead.
		if len(texts) > 1 && isSizeError(err) {
			w.splitGroup(ctx, texts, off, vecs, errs)
			return
		}
		for i := range texts {
			errs[off+i] = err
		}
		return
	}
	for i := range texts {
		if i < len(embeddings) {
			vecs[off+i] = embeddings[i]
		}
	}
}

// isSizeError reports whether err is a Voyage size-class rejection - an
// embed.APIError carrying HTTP 400 - the only failure the worker recovers from by
// splitting the batch. The proactive token budget makes such a 400 rare; it fires
// only when the character-based estimate under-counted a token-dense batch. Every
// other error is left for the retry/DLQ machinery, so an auth failure or an
// exhausted rate limit is never amplified into a split storm.
func isSizeError(err error) bool {
	var apiErr *embed.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest
}

// splitGroup embeds the two halves of texts, so a recursive split isolates a bad
// input while the rest of the group still embeds in whole batches. The two halves
// run sequentially rather than concurrently: a collected batch caps at BatchSize
// (default 128), so the split depth is tiny and the simplicity is worth more than
// the parallelism the bulk pipeline's larger super-batches needed.
func (w *Worker) splitGroup(ctx context.Context, texts []string, off int, vecs [][]float32, errs []error) {
	mid := len(texts) / 2
	w.embedGroup(ctx, texts[:mid], off, vecs, errs)
	w.embedGroup(ctx, texts[mid:], off+mid, vecs, errs)
}

// overBudget reports whether a group would exceed Voyage's per-request input
// count or estimated token budget, so it must be split before the call.
func (w *Worker) overBudget(texts []string) bool {
	if len(texts) > w.maxInputs {
		return true
	}
	tokens := 0
	for _, t := range texts {
		tokens += estimateTokens(t)
	}
	return tokens > w.maxTokens
}

// writeChunks writes the live and staging halves of a batch, each in one
// statement, and returns how many rows matched across both. A real queue is
// single-mode, so one half is normally empty and its method is skipped; routing
// both keeps a worker correct even if a queue ever carried a mix.
func (w *Worker) writeChunks(ctx context.Context, live, staging []domain.EvidenceChunk) (int, error) {
	matched := 0
	if len(live) > 0 {
		n, err := w.store.SetLiveChunkEmbeddings(ctx, live)
		if err != nil {
			return 0, err
		}
		matched += n
	}
	if len(staging) > 0 {
		n, err := w.store.SetStagingChunkEmbeddings(ctx, staging)
		if err != nil {
			return 0, err
		}
		matched += n
	}
	return matched, nil
}

// decode parses and validates one job body, logging and reporting not-ok for a
// message that can never be processed (malformed JSON or an invalid job), which
// the caller drops.
func (w *Worker) decode(ctx context.Context, body []byte) (Job, bool) {
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		w.logger.ErrorContext(ctx, "dropping malformed embedding job", slog.Any("err", err))
		return Job{}, false
	}
	if err := job.validate(); err != nil {
		w.logger.ErrorContext(ctx, "dropping invalid embedding job", slog.Any("err", err))
		return Job{}, false
	}
	return job, true
}

// applyResult applies the broker action Process or afterFailure chose. A failed
// republish requeues the original on shutdown (redelivered later) or dead-letters
// it otherwise, so a transient failure can never silently drop the job nor loop
// forever with an unadvanced attempt.
func (w *Worker) applyResult(ctx context.Context, d Delivery, res Result) {
	switch res.Action {
	case ActionRepublish:
		if err := w.enqueuer.Enqueue(ctx, res.RepublishBody, res.RepublishPriority); err != nil {
			if ctx.Err() != nil {
				w.logger.InfoContext(ctx, "re-enqueue interrupted by shutdown, requeuing original delivery", slog.Any("err", err))
				w.nack(ctx, d)
				return
			}
			w.logger.ErrorContext(ctx, "re-enqueue failed, dead-lettering original delivery", slog.Any("err", err))
			w.reject(ctx, d)
			return
		}
		w.ack(ctx, d)
	case ActionRequeue:
		w.nack(ctx, d)
	case ActionReject:
		w.reject(ctx, d)
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

// nack returns a delivery to the broker for redelivery. It is used only to
// requeue (a shutdown or an interrupted re-enqueue), so requeue is always true; a
// dead message is dead-lettered via reject, never acked away.
func (w *Worker) nack(ctx context.Context, d Delivery) {
	if err := d.Nack(true); err != nil {
		w.logger.ErrorContext(ctx, "nack failed", slog.Any("err", err))
	}
}

// reject dead-letters a delivery: a poison message or a job past its retry budget
// is parked in the DLQ via a requeue=false nack, so the loss is inspectable and
// replayable, never silent.
func (w *Worker) reject(ctx context.Context, d Delivery) {
	w.parked.Add(1)
	if err := d.Nack(false); err != nil {
		w.logger.ErrorContext(ctx, "reject (dead-letter) failed", slog.Any("err", err))
	}
}

// requeueAll nacks each delivery for redelivery, used when a shutdown abandons a
// batch the worker collected but never embedded.
func (w *Worker) requeueAll(ctx context.Context, batch []Delivery) {
	for _, d := range batch {
		w.nack(ctx, d)
	}
}

// requeueItems nacks each item's delivery for redelivery, used when a shutdown
// interrupts an in-flight batch mid-embed or mid-write.
func (w *Worker) requeueItems(ctx context.Context, items []pending) {
	for _, it := range items {
		w.nack(ctx, it.d)
	}
}

// Process embeds the job in body and writes its vector, returning the action the
// caller must take on the delivery. It never returns an error: a malformed or
// invalid message and a persistent failure are both folded into ActionReject
// (after an ERROR log, so the message is parked in the DLQ, not lost), a transient
// failure into ActionRepublish, and a shutdown into ActionRequeue.
func (w *Worker) Process(ctx context.Context, body []byte, priority uint8) Result {
	job, ok := w.decode(ctx, body)
	if !ok {
		return Result{Action: ActionReject}
	}
	return w.embedAndWrite(ctx, job, priority)
}

// embedAndWrite embeds a single job and writes its vector, the per-chunk path the
// batch falls back to when a whole-batch embed fails. It returns the action the
// caller must apply to the delivery.
func (w *Worker) embedAndWrite(ctx context.Context, job Job, priority uint8) Result {
	embeddings, err := w.embedder.EmbedDocuments(ctx, []string{job.Content})
	if err != nil {
		return w.afterFailure(ctx, job, priority, "embed", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) != domain.EmbeddingDim {
		got := 0
		if len(embeddings) == 1 {
			got = len(embeddings[0])
		}
		w.logger.ErrorContext(ctx, "dead-lettering embedding job with unexpected provider response",
			slog.String("source", job.Source), slog.String("external_id", job.ExternalID),
			slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("vectors", len(embeddings)),
			slog.Int("dims", got),
			slog.Int("want_dims", domain.EmbeddingDim))
		return Result{Action: ActionReject}
	}

	chunk := domain.EvidenceChunk{Source: job.Source, ExternalID: job.ExternalID, ChunkIndex: job.ChunkIndex, Content: job.Content, Embedding: embeddings[0]}
	matched, err := w.writeChunk(ctx, job.Staging, chunk)
	if err != nil {
		return w.afterFailure(ctx, job, priority, "write", err)
	}
	if matched == 0 {
		w.logger.InfoContext(ctx, "embedding job references a chunk no longer in the corpus, dropping",
			slog.String("source", job.Source), slog.String("external_id", job.ExternalID),
			slog.Int("chunk_index", job.ChunkIndex),
			slog.Bool("staging", job.Staging))
		return Result{Action: ActionAck}
	}
	return Result{Action: ActionAck}
}

// writeChunk writes one chunk's embedding to the staging or live corpus,
// reporting whether a row matched.
func (w *Worker) writeChunk(ctx context.Context, staging bool, chunk domain.EvidenceChunk) (int, error) {
	if staging {
		return w.store.SetStagingChunkEmbeddings(ctx, []domain.EvidenceChunk{chunk})
	}
	return w.store.SetLiveChunkEmbeddings(ctx, []domain.EvidenceChunk{chunk})
}

// afterFailure decides what to do with a job whose embed or write failed. A
// canceled context means a shutdown cut the work short: requeue it so it is
// redelivered without counting the attempt. Otherwise the attempt counts: if the
// budget is spent the job is dead-lettered with an ERROR log so the loss is
// inspectable; if attempts remain it is re-enqueued with the attempt incremented,
// at the same priority so an important chunk keeps its place.
func (w *Worker) afterFailure(ctx context.Context, job Job, priority uint8, stage string, cause error) Result {
	if ctx.Err() != nil {
		w.logger.InfoContext(ctx, "embedding job interrupted by shutdown, requeuing",
			slog.String("stage", stage),
			slog.String("source", job.Source), slog.String("external_id", job.ExternalID),
			slog.Int("chunk_index", job.ChunkIndex))
		return Result{Action: ActionRequeue}
	}

	// Compare the incoming attempt against the budget rather than Attempt+1, so a
	// corrupt or crafted job with a near-max attempt cannot overflow past the cap
	// and loop forever; validate already rejected a negative attempt.
	if job.Attempt >= w.maxAttempts-1 {
		w.logger.ErrorContext(ctx, "dead-lettering embedding job after exhausting retries",
			slog.String("stage", stage),
			slog.String("source", job.Source), slog.String("external_id", job.ExternalID),
			slog.Int("chunk_index", job.ChunkIndex),
			slog.Int("attempt", job.Attempt),
			slog.Int("max_attempts", w.maxAttempts),
			slog.Any("err", cause))
		return Result{Action: ActionReject}
	}

	retry := job
	retry.Attempt = job.Attempt + 1
	encoded, err := json.Marshal(retry)
	if err != nil {
		// Marshaling a value just unmarshaled cannot realistically fail; if it
		// does, dead-lettering is safer than spinning on a job that can never re-enqueue.
		w.logger.ErrorContext(ctx, "dead-lettering embedding job that cannot be re-encoded for retry",
			slog.String("source", job.Source), slog.String("external_id", job.ExternalID),
			slog.Int("chunk_index", job.ChunkIndex),
			slog.Any("err", err))
		return Result{Action: ActionReject}
	}
	w.logger.WarnContext(ctx, "embedding job failed, re-enqueuing for retry",
		slog.String("stage", stage),
		slog.String("source", job.Source), slog.String("external_id", job.ExternalID),
		slog.Int("chunk_index", job.ChunkIndex),
		slog.Int("next_attempt", retry.Attempt),
		slog.Int("max_attempts", w.maxAttempts),
		slog.Any("err", cause))
	return Result{Action: ActionRepublish, RepublishBody: encoded, RepublishPriority: priority}
}
