package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
)

// ErrDrainStalled is returned when the worker fleet stops embedding before
// staging is fully drained: the remaining count fails to decrease for the whole
// stall timeout. The producer aborts rather than hang or swap a partially
// embedded corpus; staging is left intact so a later run resumes.
var ErrDrainStalled = errors.New("wiki: embedding fleet stalled before draining staging")

// publishConcurrency is how many embedding jobs are published with confirms in
// flight at once. The broker client serializes each publish frame but pipelines
// the confirms, so a handful of concurrent publishers turn the enqueue from one
// confirm round-trip per chunk into a windowed stream - the difference between
// minutes and hours when priming a large corpus. It is fixed rather than tuned:
// the bound is the broker round-trip, not a resource the operator sizes.
const publishConcurrency = 16

// Publisher enqueues an embedding-job body at a priority for the worker fleet.
// The wiki producer depends only on this narrow surface; cmd/wikisync adapts the
// broker client to it, so the wiki package never imports the transport.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// ProducerStore is the staging surface the producer drives: it reads the chunks
// still to embed (in keyset order, with their metadata), watches how many remain
// while the fleet works, and finalizes the corpus once the fleet has drained.
type ProducerStore interface {
	// CountUnembeddedStaging counts the staging chunks still lacking an embedding;
	// the drain wait polls it until it reaches zero. It is a bare count so a poll
	// every few seconds stays cheap on a large staging table.
	CountUnembeddedStaging(ctx context.Context) (int64, error)
	// UnembeddedStaging returns up to limit staging chunks lacking an embedding,
	// ordered after the cursor, so the producer pages the whole un-embedded set
	// without re-reading rows the fleet has since filled.
	UnembeddedStaging(ctx context.Context, after domain.WikiCursor, limit int) ([]domain.WikiChunk, error)
	// FinalizeStaging indexes staging and swaps it into wiki_chunks atomically,
	// checkpointing the corpus at version.
	FinalizeStaging(ctx context.Context, corpus, version string, lastChangeTS time.Time, maintenanceWorkMem string, maxParallelWorkers int) error
}

// ProducerConfig tunes a bulk enqueue run. MaxPriority is the queue's priority
// ceiling that the per-chunk priority mapping is bounded by. EnqueueBatchSize is
// how many staging rows are read per keyset scan while publishing.
// DrainPollInterval is how often the producer polls the remaining count while
// the fleet embeds; DrainStallTimeout is how long the count may stand still
// before the run aborts as stalled. MaintenanceWorkMem and MaxParallelWorkers
// tune the post-drain HNSW index build, forwarded to the store untouched.
type ProducerConfig struct {
	Corpus             string
	DumpVersion        string
	MaxPriority        uint8
	EnqueueBatchSize   int
	DrainPollInterval  time.Duration
	DrainStallTimeout  time.Duration
	MaintenanceWorkMem string
	MaxParallelWorkers int
}

// EnqueueStats summarizes a completed bulk enqueue run.
type EnqueueStats struct {
	Published int
}

// priorityFor maps a staging chunk to a queue priority. When the offline
// clustering job has scored the chunk - its importance carried forward from the
// live corpus by the staging read - that semantic score drives priority, so the
// most important content embeds first. Absent a score (a new chunk, or a corpus
// not yet clustered) it falls back to the kind heuristic. Both paths are bounded
// by the queue's configured MaxPriority.
func priorityFor(c domain.WikiChunk, maxPriority uint8) uint8 {
	if c.Importance != nil {
		return priorityFromImportance(*c.Importance, maxPriority)
	}
	return priorityForKind(c.Kind, maxPriority)
}

// priorityFromImportance maps a [0,1] importance onto the queue's priority band,
// rounding to the nearest level and clamping to [0, maxPriority] in case a stored
// score drifted just outside the unit interval.
func priorityFromImportance(importance float64, maxPriority uint8) uint8 {
	scaled := math.Round(importance * float64(maxPriority))
	switch {
	case scaled < 0:
		return 0
	case scaled > float64(maxPriority):
		return maxPriority
	default:
		return uint8(scaled)
	}
}

// priorityForKind maps a chunk's VER-49 metadata to a queue priority band when no
// importance score is present. Lead sections are an article's summary - its
// highest-value evidence - so they embed first; body prose embeds after at half
// the ceiling; an unknown kind floors to zero. The result is bounded by the
// queue's configured MaxPriority and is deterministic.
func priorityForKind(kind domain.WikiChunkKind, maxPriority uint8) uint8 {
	switch kind {
	case domain.WikiChunkKindLead:
		return maxPriority
	case domain.WikiChunkKindBody:
		return maxPriority / 2
	default:
		return 0
	}
}

// RunBulkEnqueue publishes one prioritized embedding job per un-embedded staging
// chunk, waits for the worker fleet to drain the queue, then indexes and swaps
// staging into wiki_chunks atomically. It pages the un-embedded chunks in keyset
// order, so a crash mid-publish or a fleet that races ahead never re-publishes a
// chunk, and a re-run enqueues only what is still un-embedded - preserving the
// resume behavior of the old inline embed. Publishing is at-least-once with
// idempotent workers, so a duplicate is harmless. The swap fires only after
// staging is fully drained; a stalled fleet aborts the run via ErrDrainStalled
// rather than hang, and a canceled context (a -max-duration budget or SIGTERM)
// returns that cancellation as a clean resumable stop, in both cases leaving
// staging intact. A nil logger falls back to slog.Default.
func RunBulkEnqueue(ctx context.Context, logger *slog.Logger, store ProducerStore, pub Publisher, cfg ProducerConfig) (EnqueueStats, error) {
	if err := cfg.validate(); err != nil {
		return EnqueueStats{}, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	pending, err := store.CountUnembeddedStaging(ctx)
	if err != nil {
		return EnqueueStats{}, fmt.Errorf("wiki: count un-embedded staging: %w", err)
	}
	logger.InfoContext(ctx, "starting embedding-job enqueue",
		slog.String("corpus", cfg.Corpus),
		slog.Int64("pending_chunks", pending))

	published, err := publishJobs(ctx, logger, store, pub, cfg)
	if err != nil {
		return EnqueueStats{}, err
	}
	logger.InfoContext(ctx, "all embedding jobs enqueued; waiting for the worker fleet to drain staging",
		slog.String("corpus", cfg.Corpus),
		slog.Int("published", published))

	if err := waitDrained(ctx, logger, store, cfg); err != nil {
		return EnqueueStats{}, err
	}

	logger.InfoContext(ctx, "fleet drained staging; building index and swapping into wiki_chunks",
		slog.String("corpus", cfg.Corpus))
	if err := store.FinalizeStaging(ctx, cfg.Corpus, cfg.DumpVersion, parseDumpTime(cfg.DumpVersion), cfg.MaintenanceWorkMem, cfg.MaxParallelWorkers); err != nil {
		return EnqueueStats{}, fmt.Errorf("wiki: finalize staging: %w", err)
	}
	logger.InfoContext(ctx, "bulk enqueue finalized; wiki_chunks now serves the embedded corpus",
		slog.String("corpus", cfg.Corpus))
	return EnqueueStats{Published: published}, nil
}

// validate rejects a producer config that would loop, hang, or publish out of
// range, so the run fails fast at the call site rather than mid-publish.
func (cfg ProducerConfig) validate() error {
	switch {
	case cfg.MaxPriority < 1:
		return fmt.Errorf("wiki: enqueue needs a positive max priority, got %d", cfg.MaxPriority)
	case cfg.EnqueueBatchSize < 1:
		return fmt.Errorf("wiki: enqueue needs a positive batch size, got %d", cfg.EnqueueBatchSize)
	case cfg.DrainPollInterval <= 0:
		return fmt.Errorf("wiki: enqueue needs a positive drain poll interval, got %s", cfg.DrainPollInterval)
	case cfg.DrainStallTimeout < cfg.DrainPollInterval:
		return fmt.Errorf("wiki: drain stall timeout %s must be at least the poll interval %s", cfg.DrainStallTimeout, cfg.DrainPollInterval)
	default:
		return nil
	}
}

// publishJobs pages the un-embedded staging chunks in keyset order and publishes
// one embedding job per chunk at a priority derived from its metadata. Reading by
// cursor rather than by "still NULL" means a chunk the fleet embeds while the
// producer is still publishing - it embeds only what is already enqueued - is
// simply skipped on a later page, never published twice; a re-run starts the
// cursor from the beginning and the NULL filter yields only the remainder. Each
// page is published with up to publishConcurrency confirms in flight, so the
// enqueue is a windowed stream rather than one broker round-trip per chunk; the
// cursor only advances once a whole page has been confirmed, so an interrupted
// run re-enqueues at most one page of already-published (idempotent) jobs.
func publishJobs(ctx context.Context, logger *slog.Logger, store ProducerStore, pub Publisher, cfg ProducerConfig) (int, error) {
	var (
		cursor    domain.WikiCursor
		published atomic.Int64
	)
	for {
		chunks, err := store.UnembeddedStaging(ctx, cursor, cfg.EnqueueBatchSize)
		if err != nil {
			return int(published.Load()), fmt.Errorf("wiki: read un-embedded staging chunks: %w", err)
		}
		if len(chunks) == 0 {
			return int(published.Load()), nil
		}

		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(publishConcurrency)
		for _, c := range chunks {
			g.Go(func() error {
				body, err := json.Marshal(embedjob.Job{PageID: c.PageID, ChunkIndex: c.ChunkIndex, Content: c.Content})
				if err != nil {
					return fmt.Errorf("wiki: encode embedding job for page %d chunk %d: %w", c.PageID, c.ChunkIndex, err)
				}
				if err := pub.Publish(gctx, body, priorityFor(c, cfg.MaxPriority)); err != nil {
					return fmt.Errorf("wiki: publish embedding job for page %d chunk %d: %w", c.PageID, c.ChunkIndex, err)
				}
				published.Add(1)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return int(published.Load()), err
		}

		// chunks came back in keyset order, so the last is the page's high-water
		// mark; advancing only after the whole page confirmed keeps resume cheap.
		last := chunks[len(chunks)-1]
		cursor = domain.WikiCursor{PageID: last.PageID, ChunkIndex: int32(last.ChunkIndex)}
		logger.InfoContext(ctx, "enqueued embedding jobs",
			slog.String("corpus", cfg.Corpus),
			slog.Int64("published", published.Load()))
	}
}

// waitDrained blocks until staging holds no un-embedded chunk, polling the
// remaining count on a ticker. It is observable (one log line per poll carrying
// the remaining count) and bounded: if the count fails to decrease for the whole
// stall timeout the fleet is stuck, so it returns ErrDrainStalled rather than
// wait forever. The stall budget is measured against the clock since the last
// progress, so it honors DrainStallTimeout exactly whatever the poll interval
// (it stays deterministic under testing/synctest's fake clock). A canceled
// context returns that cancellation, a clean resumable stop the caller folds
// into "progress saved, re-run to resume".
func waitDrained(ctx context.Context, logger *slog.Logger, store ProducerStore, cfg ProducerConfig) error {
	remaining, err := store.CountUnembeddedStaging(ctx)
	if err != nil {
		return fmt.Errorf("wiki: poll un-embedded staging: %w", err)
	}
	if remaining == 0 {
		return nil
	}
	lastRemaining := remaining
	lastProgress := time.Now()

	ticker := time.NewTicker(cfg.DrainPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wiki: draining staging: %w", ctx.Err())
		case <-ticker.C:
			remaining, err := store.CountUnembeddedStaging(ctx)
			if err != nil {
				return fmt.Errorf("wiki: poll un-embedded staging: %w", err)
			}
			if remaining == 0 {
				logger.InfoContext(ctx, "worker fleet drained staging", slog.String("corpus", cfg.Corpus))
				return nil
			}
			if remaining < lastRemaining {
				lastRemaining = remaining
				lastProgress = time.Now()
			} else if time.Since(lastProgress) >= cfg.DrainStallTimeout {
				return fmt.Errorf("%w: %d chunks unembedded after %s without progress",
					ErrDrainStalled, remaining, cfg.DrainStallTimeout)
			}
			logger.InfoContext(ctx, "waiting for the worker fleet to drain staging",
				slog.String("corpus", cfg.Corpus),
				slog.Int64("remaining_chunks", remaining))
		}
	}
}
