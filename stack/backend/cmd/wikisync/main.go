// Command wikisync builds and maintains the Wikipedia corpus in the
// verification store. Bulk mode downloads the corpus's multistream dump,
// extracts and chunks each article's lead section, upserts the chunks, then
// publishes one prioritized embedding job per chunk to the RabbitMQ queue
// (RABBITMQ_URL) and waits for the worker fleet to embed them before swapping the
// embedded corpus into place; re-running it is idempotent and an interrupted run
// re-enqueues only the still-unembedded chunks. Embedding throughput scales with
// the worker replica count, not with this process. Delta mode asks the MediaWiki
// RecentChanges API what changed since the stored checkpoint, refetches and
// re-embeds only those articles in place (still inline, via the Voyage API and
// EMBEDDING_API_KEY), removes deleted pages, and advances the checkpoint. The
// corpus comes from WIKI_CORPUS (default simplewiki) and the database from
// DATABASE_URL. With -dry-run, bulk ingests and reports the embedding cost
// estimate without enqueuing or swapping anything, so it needs no broker. Reset
// mode clears the live corpus and its checkpoint so the next bulk run rebuilds it
// from scratch - the first step of a full reingest after the chunker or metadata
// has changed, which the dump-version checkpoint alone would not notice.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// Embedding-retry backoff bounds: a rate-limited request waits at least a
// second and at most a minute between attempts.
const (
	embedRetryBaseDelay = 1 * time.Second
	embedRetryMaxDelay  = 60 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	mode := flag.String("mode", "bulk", "sync mode: bulk (full dump ingest), delta (incremental catch-up), or reset (clear the corpus and checkpoint for a from-scratch reingest)")
	dir := flag.String("dir", os.TempDir(), "directory for downloaded dump files")
	dryRun := flag.Bool("dry-run", false, "ingest and report the embedding cost estimate without embedding or swapping")
	maxDuration := flag.Duration("max-duration", 0, "stop the run after this much wall-clock time, leaving progress for the next run to resume (0 = run to completion)")
	flag.Parse()

	err := run(logger, *mode, *dir, *dryRun, *maxDuration)
	switch {
	case err == nil:
		return
	case stoppedEarly(err):
		// A -max-duration budget or an interrupt cancels the context mid-run; the
		// embedded prefix is already committed, so this is a clean resumable stop,
		// not a failure.
		logger.Info("wikisync stopped before completing; progress saved, re-run to resume", slog.Any("reason", err))
		return
	default:
		logger.Error("wikisync failed", slog.Any("err", err))
		os.Exit(1)
	}
}

// errStoppedEarly marks a run the owned context (a -max-duration budget or an
// interrupt) cut short. The committed prefix is resumable, so this is a clean
// stop, not a failure.
var errStoppedEarly = errors.New("budget or interrupt stop")

// stoppedEarly reports whether err is the clean, resumable stop classifyStop
// produces when the owned context is canceled mid-run.
func stoppedEarly(err error) bool {
	return errors.Is(err, errStoppedEarly)
}

// classifyStop folds the owned context's state into a run's error. It reports a
// clean, resumable stop only when work was left unfinished (workErr != nil) and
// the owned context was canceled (ctxErr != nil) - a -max-duration budget or an
// interrupt cut the run short, so the committed prefix resumes next run. A run
// that finished (workErr == nil) is a success even if the budget expired at the
// buzzer: the corpus is fully built and there is nothing to resume. Any other
// error is returned unchanged. The decision is keyed on the owned context,
// never on the error shape: a transient provider timeout also satisfies
// errors.Is(err, context.DeadlineExceeded), so error-sniffing would silently
// swallow a real failure as a clean stop.
func classifyStop(workErr, ctxErr error) error {
	if workErr == nil {
		return nil
	}
	if ctxErr != nil {
		return fmt.Errorf("%w: %w", errStoppedEarly, ctxErr)
	}
	return workErr
}

func run(logger *slog.Logger, mode, dir string, dryRun bool, maxDuration time.Duration) error {
	if mode != "bulk" && mode != "delta" && mode != "reset" {
		return fmt.Errorf("wikisync: unsupported mode %q (want bulk, delta, or reset)", mode)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wikiCfg, err := config.LoadWiki()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// A positive budget caps the run's wall-clock; the deadline cancels the shared
	// context, every store and embed call unwinds, and the committed prefix stays
	// for the next run to resume from.
	if maxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, maxDuration)
		defer cancel()
	}

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	var workErr error
	switch mode {
	case "reset":
		workErr = runReset(ctx, logger, store)
	case "delta":
		workErr = runDelta(ctx, logger, store, wikiCfg, dryRun)
	default:
		workErr = runBulk(ctx, logger, store, wikiCfg, dir, dryRun)
	}
	return classifyStop(workErr, ctx.Err())
}

// runReset clears the live corpus and its sync checkpoint so the next bulk run
// rebuilds it from scratch. It is the first step of a full reingest, kept
// separate so the rebuild is explicit and so resetting needs neither the broker
// nor the embedding config. The checkpoint keys on the dump version, not the
// code, so a code change that alters chunking or metadata is invisible to a plain
// re-run (it would short-circuit as "already current"); clearing the checkpoint
// is what forces the rebuild.
func runReset(ctx context.Context, logger *slog.Logger, store *postgres.Store) error {
	logger.InfoContext(ctx, "resetting wiki corpus for a full reingest")
	if err := store.ResetWikiCorpus(ctx); err != nil {
		return err
	}
	logger.InfoContext(ctx, "wiki corpus reset; the next bulk run rebuilds it from scratch")
	return nil
}

// runBulk downloads the dump, ingests it, then either reports the embedding cost
// (dry-run) or publishes one embedding job per chunk and swaps once the worker
// fleet has drained the queue.
func runBulk(ctx context.Context, logger *slog.Logger, store *postgres.Store, wikiCfg config.Wiki, dir string, dryRun bool) error {
	// LoadWikiEmbed supplies the post-drain HNSW index-build settings the finalize
	// step forwards to the store.
	embedCfg, err := config.LoadWikiEmbed()
	if err != nil {
		return err
	}
	// A real run fans embedding out to the worker fleet over the broker, so load
	// and validate the broker and producer settings up front - a misconfiguration
	// fails before the ingest, not after it. A dry-run only estimates locally and
	// needs neither.
	var (
		queueCfg    config.Queue
		producerCfg config.WikiProducer
	)
	if !dryRun {
		if queueCfg, err = config.LoadQueue(); err != nil {
			return err
		}
		if producerCfg, err = config.LoadWikiProducer(); err != nil {
			return err
		}
	}

	logger.InfoContext(ctx, "resolving dump",
		slog.String("corpus", wikiCfg.Corpus), slog.String("dir", dir))
	var dl wiki.Downloader
	files, err := dl.Fetch(ctx, wikiCfg.Corpus, dir)
	if err != nil {
		return err
	}
	resolution := "dump downloaded"
	if files.Reused {
		resolution = "reusing existing dump"
	}
	logger.InfoContext(ctx, resolution,
		slog.String("dump", files.DumpPath), slog.String("version", files.Version))

	plan, err := store.StagingPlan(ctx, files.Version)
	if err != nil {
		return err
	}
	if plan == wiki.PlanAlreadyCurrent {
		logger.InfoContext(ctx, "corpus already current; nothing to do",
			slog.String("corpus", wikiCfg.Corpus), slog.String("version", files.Version))
		return nil
	}
	if plan == wiki.PlanBuild {
		ingestStats, err := wiki.RunBulk(ctx, store, files, wikiCfg.Corpus)
		if err != nil {
			return err
		}
		logger.InfoContext(ctx, "ingest complete",
			slog.String("corpus", wikiCfg.Corpus),
			slog.Int("pages_seen", ingestStats.PagesSeen),
			slog.Int("pages_skipped", ingestStats.PagesSkipped),
			slog.Int("pages_stored", ingestStats.PagesStored),
			slog.Int("chunks", ingestStats.Chunks),
			slog.Int64("embeddings_carried", ingestStats.Carried))
	} else {
		logger.InfoContext(ctx, "resuming enqueue of staged corpus",
			slog.String("corpus", wikiCfg.Corpus), slog.String("version", files.Version))
	}

	if dryRun {
		est, err := wiki.EstimateBulkEmbed(ctx, store)
		if err != nil {
			return err
		}
		logger.InfoContext(ctx, "embedding cost estimate (dry run)",
			slog.String("corpus", wikiCfg.Corpus),
			slog.Int64("pages", est.Pages),
			slog.Int64("chunks", est.Chunks),
			slog.Int64("estimated_tokens", est.Tokens),
			slog.Float64("estimated_cost_usd", est.CostUSD))
		return nil
	}

	client, err := queue.New(queue.Config{
		URL:         queueCfg.URL,
		QueueName:   queueCfg.Name,
		MaxPriority: queueCfg.MaxPriority,
		Prefetch:    queueCfg.Prefetch,
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	stats, err := wiki.RunBulkEnqueue(ctx, logger, store, qPublisher{client: client}, wiki.ProducerConfig{
		Corpus:             wikiCfg.Corpus,
		DumpVersion:        files.Version,
		MaxPriority:        queueCfg.MaxPriority,
		EnqueueBatchSize:   producerCfg.EnqueueBatchSize,
		DrainPollInterval:  producerCfg.DrainPollInterval,
		DrainStallTimeout:  producerCfg.DrainStallTimeout,
		MaintenanceWorkMem: embedCfg.MaintenanceWorkMem,
		MaxParallelWorkers: embedCfg.MaxParallelWorkers,
	})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "bulk enqueue complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("published_chunks", stats.Published))
	return nil
}

// runDelta catches the corpus up to the live wiki incrementally via the
// MediaWiki API, embedding only what changed.
func runDelta(ctx context.Context, logger *slog.Logger, store *postgres.Store, wikiCfg config.Wiki, dryRun bool) error {
	if dryRun {
		return errors.New("wikisync: -dry-run is only supported for bulk mode")
	}
	deltaCfg, err := config.LoadWikiDelta()
	if err != nil {
		return err
	}
	embedCfg, err := config.LoadWikiEmbed()
	if err != nil {
		return err
	}
	embProvider, err := config.LoadEmbedding()
	if err != nil {
		return err
	}

	api := &wiki.APIClient{Corpus: wikiCfg.Corpus}
	logger.InfoContext(ctx, "starting delta sync", slog.String("corpus", wikiCfg.Corpus))
	stats, err := wiki.RunDelta(ctx, store, api, newEmbedder(logger, embProvider, embedCfg), wiki.DeltaConfig{
		Corpus:        wikiCfg.Corpus,
		RetentionDays: deltaCfg.RetentionDays,
		BulkFraction:  deltaCfg.BulkFraction,
		BatchSize:     embedCfg.BatchSize,
		Concurrency:   embedCfg.Concurrency,
	}, time.Now().UTC())
	if err != nil {
		return err
	}
	if stats.RecommendBulk {
		logger.WarnContext(ctx, "change set exceeds the bulk threshold; a -mode=bulk re-run would rebuild the index more cleanly",
			slog.String("corpus", wikiCfg.Corpus))
	}
	logger.InfoContext(ctx, "delta sync complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("pages_changed", stats.Changed),
		slog.Int("pages_skipped", stats.Skipped),
		slog.Int("pages_deleted", stats.Deleted),
		slog.Int("embedded_chunks", stats.Embedded))
	return nil
}

// newEmbedder builds the Voyage embedding client wrapped in the shared retry
// decorator both sync modes use, optionally paced to a per-minute request budget
// for a constrained tier. The rate limiter sits beneath the retry decorator so
// every attempt - including retries - is paced; the logger lets the retry
// decorator surface backoffs, so a throttled or stalled run is visible rather
// than silently waiting.
func newEmbedder(logger *slog.Logger, p config.Embedding, embedCfg config.WikiEmbed) *embed.RetryClient {
	return embed.WithRetry(
		embed.WithRateLimit(
			embed.New(embed.Config{
				APIKey:     p.APIKey,
				Model:      p.Model,
				Dim:        p.Dim,
				HTTPClient: &http.Client{Timeout: embedCfg.HTTPTimeout},
			}),
			embedCfg.RequestsPerMinute,
		),
		embed.RetryConfig{MaxAttempts: embedCfg.MaxRetries, BaseDelay: embedRetryBaseDelay, MaxDelay: embedRetryMaxDelay, Logger: logger},
	)
}
