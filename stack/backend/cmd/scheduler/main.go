// Command scheduler is the ingestion fleet's always-on local cron scheduler. It
// iterates the connector registry (internal/connector) - the one table of every
// source - and, for each schedulable source an operator has enabled, builds its
// producer and registers it on its own configurable cron cadence, running them all
// until a signal arrives. Every run publishes into its RabbitMQ queue for the
// worker fleet to drain, an overlapping tick of a source whose previous run is
// still in flight is skipped, and each run posts the shared start/finish/error
// Slack alerts through crawlnotify.
//
// It is a thin wiring layer over internal/schedule (the runtime-agnostic registry
// and tick loop) so a future cloud runner can reuse the same registry. Adding a
// source needs no edit here beyond one builders-table entry: the source's name,
// cron default, and enable knob come from its connector descriptor. Per-source
// enable flags and cron specs come from the environment (SCHEDULE_<PREFIX>_*),
// validated at startup; an invalid cron spec fails fast. The broker comes from
// RABBITMQ_URL, and each source reads the same producer config its standalone cmd
// does.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/claimreviewsite"
	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/datacommons"
	"github.com/verovec/truth-in-stream/backend/internal/evidencegate"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/schedule"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsarchive"
	"github.com/verovec/truth-in-stream/backend/internal/source/evidencesrc"
	"github.com/verovec/truth-in-stream/backend/internal/source/hatvp"
	"github.com/verovec/truth-in-stream/backend/internal/source/parliament"
	"github.com/verovec/truth-in-stream/backend/internal/source/viepublique"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// crawlHTTPTimeout bounds each upstream API request a producer makes; a slow
// endpoint fails the request rather than stalling the run.
const crawlHTTPTimeout = 30 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("scheduler exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

// producerBuilder constructs a source's producer and a closer for the broker
// client it opens. It is the one wiring step the connector registry cannot
// declare, because only the cmd layer may import the broker and the producer
// config. On its own error it closes whatever it already opened before returning.
type producerBuilder func(logger *slog.Logger) (crawlnotify.Producer, func(), error)

// builders maps each schedulable source name to its producer builder. Adding a
// scheduled source is one entry here plus its connector descriptor - no other edit
// to this file. TestBuildersCoverSchedulableSources guards that this table and the
// registry's schedulable sources stay in lockstep.
var builders = buildersTable()

// buildersTable maps each schedulable source to its producer builder. The several
// parliament datasets share one builder parameterized by the descriptor name (the
// scheduler runs them all in one process, so the dataset comes from the registry,
// not the single PARLIAMENT_DATASET env the host uses). Adding a scheduled source is
// one entry here plus its connector descriptor.
func buildersTable() map[string]producerBuilder {
	b := map[string]producerBuilder{
		"wikipedia":   buildWikipedia,
		"factcheck":   buildFactcheck,
		"scrutins":    buildScrutins,
		"datacommons": buildDatacommons,
		"claimreview": buildClaimreview,
		"viepublique": buildViePublique,
		"hatvp":       buildHATVP,
	}
	for _, dataset := range parliament.Datasets() {
		b[dataset] = buildParliament(dataset)
	}
	return b
}

// scheduleSpecs derives the config specs from the registry's schedulable sources,
// so config reads a SCHEDULE_<PREFIX>_* knob for every source without importing the
// registry.
func scheduleSpecs() []config.ScheduleSpec {
	var specs []config.ScheduleSpec
	for _, d := range connector.All() {
		if !d.Schedulable() {
			continue
		}
		specs = append(specs, config.ScheduleSpec{Name: d.Name, EnvPrefix: d.EnvPrefix(), DefaultCron: d.DefaultCron})
	}
	return specs
}

// run builds the registry by iterating the connector registry, wires the notifier,
// and runs the scheduler until a signal. Every broker client it opens is closed on
// shutdown via the collected closers. A registry with no enabled source is not an
// error: the always-on service idles until a signal rather than crash-looping a
// restart-on-exit container.
func run(logger *slog.Logger) error {
	scheduleCfg, err := config.LoadSchedule(scheduleSpecs())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var reg schedule.Registry
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()

	var enabled []string
	for _, d := range connector.All() {
		if !d.Schedulable() {
			continue
		}
		src, ok := scheduleCfg.Source(d.Name)
		if !ok || !src.Enabled {
			continue
		}
		build, ok := builders[d.Name]
		if !ok {
			return fmt.Errorf("scheduler: no producer builder for schedulable source %q", d.Name)
		}
		sched, specErr := schedule.ParseSpec(src.Cron)
		if specErr != nil {
			return fmt.Errorf("%s: %w", d.Name, specErr)
		}
		producer, closer, buildErr := build(logger)
		if buildErr != nil {
			return buildErr
		}
		closers = append(closers, closer)
		// The producer must self-identify as its descriptor name: the enable/cron
		// config was resolved by d.Name, and the run alerts key off producer.Name(),
		// so a divergence would alert under a name the operator never configured.
		if producer.Name() != d.Name {
			return fmt.Errorf("scheduler: source %q builds a producer that names itself %q", d.Name, producer.Name())
		}
		if regErr := reg.RegisterSchedule(d.Name, sched, scheduleCfg.Jitter, producer); regErr != nil {
			return regErr
		}
		enabled = append(enabled, d.Name)
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)
	s := schedule.New(reg, notifier, logger)

	logger.InfoContext(ctx, "scheduler started",
		slog.Int("sources", reg.Len()),
		slog.Any("enabled", enabled),
		slog.Duration("jitter", scheduleCfg.Jitter))

	if reg.Len() == 0 {
		// The always-on service idles when every source is disabled rather than
		// crash-looping; an operator enables a source with SCHEDULE_<PREFIX>_ENABLED.
		logger.InfoContext(ctx, "scheduler idle: no source enabled, waiting for signal")
	}

	s.Run(ctx)

	logger.InfoContext(ctx, "scheduler stopped")
	return nil
}

// buildWikipedia builds the Wikipedia crawl producer from its config, returning a
// closer for the broker client it opens. On its own error it closes what it opened.
func buildWikipedia(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
	crawlCfg, err := config.LoadCrawl()
	if err != nil {
		return nil, nil, err
	}
	queueCfg, err := config.LoadCrawlQueue()
	if err != nil {
		return nil, nil, err
	}
	gateCfg, err := config.LoadCrawlCheckworthy()
	if err != nil {
		return nil, nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, nil, err
	}
	closer := func() { _ = client.Close() }

	var gate wiki.Gate
	if gateCfg.Active() {
		gateClient, gateErr := evidencegate.New(evidencegate.Config{
			Provider:     llm.ProviderName(gateCfg.Provider),
			APIKey:       gateCfg.APIKey,
			GeminiAPIKey: gateCfg.GeminiAPIKey, DeepSeekAPIKey: gateCfg.DeepSeekAPIKey,
			Model: gateCfg.Model,
		})
		if gateErr != nil {
			closer()
			return nil, nil, gateErr
		}
		gate = gateClient
	}

	categories := wiki.ShardCategories(crawlCfg.Categories, crawlCfg.Shards, crawlCfg.ShardIndex)
	if len(categories) == 0 {
		closer()
		return nil, nil, fmt.Errorf("scheduler: wikipedia shard has no categories (shards=%d index=%d)", crawlCfg.Shards, crawlCfg.ShardIndex)
	}

	checkpoint, err := wiki.LoadCheckpoint(crawlCfg.CheckpointPath)
	if err != nil {
		closer()
		return nil, nil, fmt.Errorf("scheduler: load crawl checkpoint: %w", err)
	}
	gateFailMode := wiki.GateFailOpen
	if crawlCfg.GateFailClosed {
		gateFailMode = wiki.GateFailClosed
	}

	producer := wikiProducer{
		run:    wiki.RunCrawl,
		logger: logger,
		src:    &wiki.APIClient{Corpus: crawlCfg.Project, HTTPClient: &http.Client{Timeout: crawlHTTPTimeout}},
		pub:    qPublisher{client: client},
		gate:   gate,
		cfg: wiki.CrawlConfig{
			Categories:      categories,
			Corpus:          crawlCfg.Corpus,
			Project:         crawlCfg.Project,
			MaxDepth:        crawlCfg.MaxDepth,
			MaxPages:        crawlCfg.MaxPages,
			IncludeBody:     crawlCfg.IncludeBody,
			MaxPriority:     queueCfg.MaxPriority,
			GateConcurrency: gateCfg.Concurrency,
			GateRPM:         gateCfg.RPM,
			GateFailMode:    gateFailMode,
			ErrorBudget:     crawlCfg.ErrorBudget,
			Checkpoint:      checkpoint,
		},
	}
	return producer, closer, nil
}

// buildFactcheck builds the fact-check-archive producer.
func buildFactcheck(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
	archiveCfg, err := config.LoadFactCheckArchive()
	if err != nil {
		return nil, nil, err
	}
	queueCfg, err := config.LoadFactCheckQueue()
	if err != nil {
		return nil, nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, nil, err
	}
	closer := func() { _ = client.Close() }

	archive, err := factcheckarchive.New(factcheckarchive.Config{
		APIKey:       archiveCfg.APIKey,
		LanguageCode: archiveCfg.Language,
		MaxPriority:  queueCfg.MaxPriority,
	})
	if err != nil {
		closer()
		return nil, nil, err
	}

	checkpoint, err := factcheckarchive.LoadStreamCheckpoint(archiveCfg.CheckpointPath)
	if err != nil {
		closer()
		return nil, nil, err
	}

	producer := factcheckProducer{
		client: archive,
		logger: logger,
		pub:    qPublisher{client: client},
		streams: factcheckarchive.BuildStreams(factcheckarchive.Strategy{
			Topics:         archiveCfg.Topics,
			PublisherSites: archiveCfg.PublisherSites,
			MaxPages:       archiveCfg.MaxPages,
			MaxAgeDays:     archiveCfg.MaxAgeDays,
		}),
		checkpoint: checkpoint,
	}
	return producer, closer, nil
}

// buildDatacommons builds the DataCommons ClaimReview feed producer. It publishes
// to the same factcheck.claims queue the Google-API factcheck source uses, so it
// reads the fact-check queue config; the feed itself is keyless.
func buildDatacommons(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
	archiveCfg, err := config.LoadDataCommonsArchive()
	if err != nil {
		return nil, nil, err
	}
	queueCfg, err := config.LoadFactCheckQueue()
	if err != nil {
		return nil, nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, nil, err
	}
	closer := func() { _ = client.Close() }

	feed, err := datacommons.New(datacommons.Config{
		FeedURL:         archiveCfg.FeedURL,
		OutletAllowlist: archiveCfg.OutletAllowlist,
		MaxItems:        archiveCfg.MaxItems,
		Format:          archiveCfg.Format,
		MaxPriority:     queueCfg.MaxPriority,
	})
	if err != nil {
		closer()
		return nil, nil, err
	}

	producer := datacommonsProducer{
		client:  feed,
		logger:  logger,
		pub:     qPublisher{client: client},
		outlets: archiveCfg.OutletAllowlist,
	}
	return producer, closer, nil
}

// buildClaimreview builds the ClaimReview JSON-LD outlet reader. It publishes to the
// same factcheck.claims queue the factcheck source uses; the outlets are public.
func buildClaimreview(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
	sitesCfg, err := config.LoadClaimReviewSites()
	if err != nil {
		return nil, nil, err
	}
	queueCfg, err := config.LoadFactCheckQueue()
	if err != nil {
		return nil, nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, nil, err
	}
	closer := func() { _ = client.Close() }

	outlets := make([]claimreviewsite.Outlet, 0, len(sitesCfg.Outlets))
	for _, o := range sitesCfg.Outlets {
		outlets = append(outlets, claimreviewsite.Outlet{Name: o.Name, Host: o.Host, Sitemap: o.Sitemap})
	}
	reader, err := claimreviewsite.New(claimreviewsite.Config{
		Outlets:          outlets,
		UserAgent:        sitesCfg.UserAgent,
		MinDelay:         sitesCfg.MinDelay,
		MaxURLsPerOutlet: sitesCfg.MaxURLsPerOutlet,
		MaxPriority:      queueCfg.MaxPriority,
	})
	if err != nil {
		closer()
		return nil, nil, err
	}

	producer := claimreviewProducer{
		client:  reader,
		logger:  logger,
		pub:     qPublisher{client: client},
		outlets: len(outlets),
	}
	return producer, closer, nil
}

// buildScrutins builds the scrutins-archive producer.
func buildScrutins(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
	archiveCfg, err := config.LoadScrutinsArchive()
	if err != nil {
		return nil, nil, err
	}
	queueCfg, err := config.LoadScrutinsQueue()
	if err != nil {
		return nil, nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, nil, err
	}
	closer := func() { _ = client.Close() }

	producer, err := scrutinsarchive.New(scrutinsarchive.Config{
		Legislature: archiveCfg.Legislature,
		MarkerPath:  archiveCfg.MarkerPath,
		MaxPriority: queueCfg.MaxPriority,
	}, qPublisher{client: client}, logger)
	if err != nil {
		closer()
		return nil, nil, err
	}
	return producer, closer, nil
}

// buildViePublique builds the vie-publique discours metadata producer, a keyless
// dump connector publishing generic evidence jobs to the evidence queue.
func buildViePublique(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
	cfg, err := config.LoadViePublique()
	if err != nil {
		return nil, nil, err
	}
	queueCfg, err := config.LoadEvidenceQueue()
	if err != nil {
		return nil, nil, err
	}
	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, nil, err
	}
	closer := func() { _ = client.Close() }
	producer, err := evidencesrc.NewDumpProducer(evidencesrc.DumpConfig{
		Source:       viepublique.Source,
		URL:          viepublique.DumpURL,
		Scope:        "vie-publique discours (metadonnees)",
		Extract:      viepublique.Extract,
		MarkerPath:   cfg.MarkerPath,
		ManifestPath: cfg.ManifestPath,
		MaxPriority:  queueCfg.MaxPriority,
		MaxItems:     cfg.MaxItems,
	}, qPublisher{client: client}, logger)
	if err != nil {
		closer()
		return nil, nil, err
	}
	return producer, closer, nil
}

// buildHATVP builds the HATVP declarations producer, a keyless index+detail
// connector publishing generic evidence jobs to the evidence queue.
func buildHATVP(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
	cfg, err := config.LoadHATVP()
	if err != nil {
		return nil, nil, err
	}
	queueCfg, err := config.LoadEvidenceQueue()
	if err != nil {
		return nil, nil, err
	}
	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, nil, err
	}
	closer := func() { _ = client.Close() }
	producer, err := hatvp.New(hatvp.Config{
		MarkerPath:   cfg.MarkerPath,
		ManifestPath: cfg.ManifestPath,
		MaxPriority:  queueCfg.MaxPriority,
		MaxItems:     cfg.MaxItems,
	}, qPublisher{client: client}, logger)
	if err != nil {
		closer()
		return nil, nil, err
	}
	return producer, closer, nil
}

// buildParliament builds the producer builder for one parliament dataset. The Senat
// scrutins dataset publishes to the scrutins queue drained by the scrutins worker;
// every textual dataset publishes to the evidence queue drained by the evidence
// worker.
func buildParliament(dataset string) producerBuilder {
	return func(logger *slog.Logger) (crawlnotify.Producer, func(), error) {
		parliamentCfg, err := config.LoadParliamentFor(dataset)
		if err != nil {
			return nil, nil, err
		}
		queueCfg, err := parliamentQueueCfg(dataset)
		if err != nil {
			return nil, nil, err
		}

		client, err := queue.New(queueCfg.ClientConfig(0))
		if err != nil {
			return nil, nil, err
		}
		closer := func() { _ = client.Close() }

		producer, err := parliament.New(parliament.Config{
			Dataset:      parliamentCfg.Dataset,
			Legislature:  parliamentCfg.Legislature,
			MarkerPath:   parliamentCfg.MarkerPath,
			ManifestPath: parliamentCfg.ManifestPath,
			MaxPriority:  queueCfg.MaxPriority,
			MaxItems:     parliamentCfg.MaxItems,
			SinceYear:    parliamentCfg.SinceYear,
		}, qPublisher{client: client}, logger)
		if err != nil {
			closer()
			return nil, nil, err
		}
		return producer, closer, nil
	}
}

// parliamentQueueCfg binds a parliament dataset to its queue.
func parliamentQueueCfg(dataset string) (config.Queue, error) {
	if parliament.IsVotingDataset(dataset) {
		return config.LoadScrutinsQueue()
	}
	return config.LoadEvidenceQueue()
}
