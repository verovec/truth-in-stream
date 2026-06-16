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
	"github.com/verovec/truth-in-stream/backend/internal/domain"
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
	publishOnly := flag.Bool("publish-only", false, "bulk mode only: publish embedding jobs and exit (the cloud producer path). The default bulk-into-live ingest already publishes and exits, so this only changes an -atomic run, where it skips the drain and swap so the consumer owns them")
	atomic := flag.Bool("atomic", false, "bulk mode only: build the corpus in a staging table and swap it in atomically once fully embedded, instead of the default bulk-into-live ingest that makes chunks searchable as they embed; use it for a wholesale re-chunk cutover")
	maxDuration := flag.Duration("max-duration", 0, "stop the run after this much wall-clock time, leaving progress for the next run to resume (0 = run to completion)")
	flag.Parse()

	err := run(logger, *mode, *dir, *dryRun, *publishOnly, *atomic, *maxDuration)
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

func run(logger *slog.Logger, mode, dir string, dryRun, publishOnly, atomic bool, maxDuration time.Duration) error {
	if mode != "bulk" && mode != "delta" && mode != "reset" {
		return fmt.Errorf("wikisync: unsupported mode %q (want bulk, delta, or reset)", mode)
	}
	if publishOnly && mode != "bulk" {
		return fmt.Errorf("wikisync: -publish-only is only supported for bulk mode, got %q", mode)
	}
	if publishOnly && dryRun {
		return errors.New("wikisync: -publish-only and -dry-run are mutually exclusive")
	}
	if atomic && mode != "bulk" {
		return fmt.Errorf("wikisync: -atomic is only supported for bulk mode, got %q", mode)
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
		workErr = runBulk(ctx, logger, store, wikiCfg, dir, dryRun, publishOnly, atomic)
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

// runBulk downloads the dump and ingests it. By default it ingests straight into
// the live table and publishes one embedding job per chunk, so the corpus is
// queryable and grows as the fleet embeds (bulk-into-live). With -atomic it
// builds the corpus in a staging table and swaps it in once fully embedded, the
// wholesale-cutover path. A dry-run reports the embedding cost without writing or
// publishing.
func runBulk(ctx context.Context, logger *slog.Logger, store *postgres.Store, wikiCfg config.Wiki, dir string, dryRun, publishOnly, atomic bool) error {
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

	if atomic {
		return runBulkAtomic(ctx, logger, store, wikiCfg, files, plan, dryRun, publishOnly)
	}
	return runBulkLive(ctx, logger, store, wikiCfg, files, dryRun)
}

// runBulkLive ingests the dump straight into the live corpus and publishes the
// un-embedded chunks for the fleet, which fills them in place on the live HNSW
// index - no staging, no swap. The corpus is queryable throughout; the published
// jobs grow it monotonically as they embed. A dry-run reports the cost of the
// chunks still pending in the live corpus without ingesting or publishing.
func runBulkLive(ctx context.Context, logger *slog.Logger, store *postgres.Store, wikiCfg config.Wiki, files wiki.DumpFiles, dryRun bool) error {
	if dryRun {
		// Dry-run never writes, so it reports the cost of what is already pending in
		// the live corpus rather than a fresh parse; use -atomic -dry-run for a
		// full pre-build estimate of an unbuilt corpus.
		rem, err := store.LiveRemaining(ctx)
		if err != nil {
			return err
		}
		logEstimate(ctx, logger, wikiCfg.Corpus, wiki.EstimateFromRemaining(rem))
		return nil
	}

	queueCfg, err := config.LoadQueue()
	if err != nil {
		return err
	}
	producerCfg, err := config.LoadWikiProducer()
	if err != nil {
		return err
	}

	ingestStats, err := wiki.RunBulkLive(ctx, store, files, wikiCfg.Corpus)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "bulk-into-live ingest complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("pages_seen", ingestStats.PagesSeen),
		slog.Int("pages_skipped", ingestStats.PagesSkipped),
		slog.Int("pages_stored", ingestStats.PagesStored),
		slog.Int("chunks", ingestStats.Chunks))

	// Record the dump version now serving so a re-run can tell the corpus is
	// current (liveCurrentAt also requires zero un-embedded chunks, so this never
	// reports current while the fleet is still filling in vectors).
	lastChange, _ := http.ParseTime(files.Version)
	if err := store.SetSyncState(ctx, domain.WikiSyncState{Corpus: wikiCfg.Corpus, DumpVersion: files.Version, LastChangeTS: lastChange}); err != nil {
		return err
	}

	client, err := queue.New(queue.Config{
		URL:         queueCfg.URL,
		QueueName:   queueCfg.VersionedName(),
		Version:     queueCfg.Version,
		MaxPriority: queueCfg.MaxPriority,
		Prefetch:    queueCfg.Prefetch,
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	stats, err := wiki.RunBulkLivePublish(ctx, logger, store, qPublisher{client: client}, wiki.ProducerConfig{
		Corpus:           wikiCfg.Corpus,
		DumpVersion:      files.Version,
		MaxPriority:      queueCfg.MaxPriority,
		EnqueueBatchSize: producerCfg.EnqueueBatchSize,
	})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "bulk-into-live enqueue complete; the fleet fills the live corpus incrementally",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("published_chunks", stats.Published))
	return nil
}

// runBulkAtomic builds the next corpus in a staging table and swaps it into place
// once the fleet has fully embedded it, the wholesale-cutover path. It is the
// pre-existing behavior, kept for a breaking re-chunk where serving a mix of old
// and new chunks is unacceptable. With publish-only it fills the queue and exits,
// leaving the drain and swap to the consumer (the cloud producer path).
func runBulkAtomic(ctx context.Context, logger *slog.Logger, store *postgres.Store, wikiCfg config.Wiki, files wiki.DumpFiles, plan wiki.BulkPlan, dryRun, publishOnly bool) error {
	// LoadWikiEmbed supplies the post-drain HNSW index-build settings the finalize
	// step forwards to the store.
	embedCfg, err := config.LoadWikiEmbed()
	if err != nil {
		return err
	}
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
		logEstimate(ctx, logger, wikiCfg.Corpus, est)
		return nil
	}

	// The producer publishes to the active versioned queue resolved from the same
	// configuration the worker consumes, so both bind to the same queue without
	// touching the enqueue logic.
	client, err := queue.New(queue.Config{
		URL:         queueCfg.URL,
		QueueName:   queueCfg.VersionedName(),
		Version:     queueCfg.Version,
		MaxPriority: queueCfg.MaxPriority,
		Prefetch:    queueCfg.Prefetch,
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	producer := wiki.ProducerConfig{
		Corpus:             wikiCfg.Corpus,
		DumpVersion:        files.Version,
		MaxPriority:        queueCfg.MaxPriority,
		EnqueueBatchSize:   producerCfg.EnqueueBatchSize,
		DrainPollInterval:  producerCfg.DrainPollInterval,
		DrainStallTimeout:  producerCfg.DrainStallTimeout,
		MaintenanceWorkMem: embedCfg.MaintenanceWorkMem,
		MaxParallelWorkers: embedCfg.MaxParallelWorkers,
	}

	// The cloud producer fills the queue and exits; the consumer (a worker against
	// the database it writes to) owns the drain and the live swap. The default
	// co-located run publishes, waits for the fleet to drain, and swaps itself.
	if publishOnly {
		stats, err := wiki.RunBulkPublish(ctx, logger, store, qPublisher{client: client}, producer)
		if err != nil {
			return err
		}
		logger.InfoContext(ctx, "publish-only enqueue complete; the consumer will drain and swap",
			slog.String("corpus", wikiCfg.Corpus),
			slog.Int("published_chunks", stats.Published))
		return nil
	}

	stats, err := wiki.RunBulkEnqueue(ctx, logger, store, qPublisher{client: client}, producer)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "bulk enqueue complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("published_chunks", stats.Published))
	return nil
}

// logEstimate reports a dry-run embedding cost projection.
func logEstimate(ctx context.Context, logger *slog.Logger, corpus string, est wiki.Estimate) {
	logger.InfoContext(ctx, "embedding cost estimate (dry run)",
		slog.String("corpus", corpus),
		slog.Int64("pages", est.Pages),
		slog.Int64("chunks", est.Chunks),
		slog.Int64("estimated_tokens", est.Tokens),
		slog.Float64("estimated_cost_usd", est.CostUSD))
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
