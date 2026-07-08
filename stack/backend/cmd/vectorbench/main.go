// Command vectorbench runs the datastore scale benchmark: it generates a
// deterministic synthetic corpus, loads it into a pgvector database, and
// measures recall@k, p50/p95 latency, and on-disk footprint across index
// strategies (halfvec HNSW, binary-quantize + rerank, partition-by-source),
// sweeping hnsw.ef_search and hnsw.iterative_scan. The numbers feed the
// datastore-at-scale verdict (docs/datastore-scale-benchmark.md). It is
// offline tooling: point it at a throwaway database (BENCH_DATABASE_URL,
// falling back to DATABASE_URL); it owns the vectorbench schema and never
// touches application tables.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/vectorbench"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("vectorbench exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	rows := flag.Int("rows", 100000, "corpus size in vectors")
	queries := flag.Int("queries", 100, "number of benchmark queries")
	k := flag.Int("k", 10, "neighbors returned per search")
	dims := flag.Int("dims", 1024, "embedding dimension")
	sources := flag.Int("sources", 8, "number of synthetic sources")
	clusters := flag.Int("clusters", 64, "number of Gaussian clusters")
	seed := flag.Uint64("seed", 42, "corpus generation seed")
	scenarios := flag.String("scenarios", "hnsw,bq-rerank,partitioned", "scenarios to measure")
	filters := flag.String("filters", "none,source", "query access patterns")
	efSearch := flag.String("ef", "40,100,200,400", "hnsw.ef_search sweep")
	iterative := flag.String("iterative", "off,relaxed_order", "hnsw.iterative_scan sweep")
	multipliers := flag.String("multipliers", "5,10,20", "bq-rerank candidate multipliers (x k)")
	keep := flag.Bool("keep", false, "keep the vectorbench schema after the run")
	out := flag.String("out", "", "write the report as markdown to this path")
	flag.Parse()

	cfg, err := buildConfig(*rows, *queries, *k, *dims, *sources, *clusters, *seed,
		*scenarios, *filters, *efSearch, *iterative, *multipliers, *keep)
	if err != nil {
		return err
	}
	dsn := os.Getenv("BENCH_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return errors.New("BENCH_DATABASE_URL (or DATABASE_URL) is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer pool.Close()

	report, err := vectorbench.Run(ctx, pool, cfg, logger)
	if err != nil {
		return err
	}
	fmt.Print(vectorbench.RenderText(report.Results, report.Footprints))
	if *out != "" {
		md := reportHeader(cfg, report.PgvectorVersion) +
			vectorbench.RenderMarkdown(report.Results, report.Footprints)
		if err := os.WriteFile(*out, []byte(md), 0o644); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
		logger.Info("report written", slog.String("path", *out))
	}
	return nil
}

func buildConfig(rows, queries, k, dims, sources, clusters int, seed uint64,
	scenarios, filters, efSearch, iterative, multipliers string, keep bool,
) (vectorbench.Config, error) {
	parsedScenarios, err := vectorbench.ParseScenarios(scenarios)
	if err != nil {
		return vectorbench.Config{}, err
	}
	parsedFilters, err := vectorbench.ParseFilters(filters)
	if err != nil {
		return vectorbench.Config{}, err
	}
	parsedEf, err := vectorbench.ParseInts(efSearch)
	if err != nil {
		return vectorbench.Config{}, err
	}
	parsedIterative, err := vectorbench.ParseIterative(iterative)
	if err != nil {
		return vectorbench.Config{}, err
	}
	parsedMultipliers, err := vectorbench.ParseInts(multipliers)
	if err != nil {
		return vectorbench.Config{}, err
	}
	return vectorbench.Config{
		Corpus: vectorbench.CorpusConfig{
			Rows:     rows,
			Queries:  queries,
			Dims:     dims,
			Sources:  sources,
			Clusters: clusters,
			Seed:     seed,
		},
		Matrix: vectorbench.MatrixConfig{
			Scenarios:   parsedScenarios,
			Filters:     parsedFilters,
			EfSearch:    parsedEf,
			Iterative:   parsedIterative,
			Multipliers: parsedMultipliers,
		},
		K:    k,
		Keep: keep,
	}, nil
}

func reportHeader(cfg vectorbench.Config, version string) string {
	return fmt.Sprintf(
		"# Datastore scale benchmark report\n\npgvector %s; rows=%d queries=%d k=%d dims=%d sources=%d clusters=%d seed=%d\n\n",
		version, cfg.Corpus.Rows, cfg.Corpus.Queries, cfg.K, cfg.Corpus.Dims,
		cfg.Corpus.Sources, cfg.Corpus.Clusters, cfg.Corpus.Seed,
	)
}
