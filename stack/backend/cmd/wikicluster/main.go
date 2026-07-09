// Command wikicluster groups the embedded wiki corpus into topic clusters and
// scores each chunk's importance, writing cluster_id and importance back into the
// live table so the next ingest's producer embeds the most important content
// first. It is an offline batch step with no HTTP surface: run it after a corpus
// is embedded, and re-running it over an unchanged corpus is idempotent (the
// clustering is deterministically seeded). The database comes from DATABASE_URL;
// WIKI_CLUSTER_* tunes the cluster count, iterations, seed, and batch sizes.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/cluster"
	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("wiki clustering exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	clusterCfg, err := config.LoadWikiCluster()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	_, err = clusterCorpus(ctx, logger, store, clusterCfg)
	return err
}

// clusterStore is the slice of the store the clustering job drives: it reads the
// embedded corpus and writes each chunk's cluster id and importance back.
type clusterStore interface {
	EmbeddedChunks(ctx context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error)
	SetChunkClustering(ctx context.Context, chunks []domain.EvidenceChunk) error
}

// stats summarizes a clustering run for the operator: how many chunks were
// scored, how many clusters they fell into, the spread of importance scores, and
// the largest cluster's size. They make the run observable (the card's
// requirement) so a degenerate clustering - say everything in one cluster - is
// visible in the logs.
type stats struct {
	Chunks         int
	Clusters       int
	LargestCluster int
	MinImportance  float64
	MeanImportance float64
	MaxImportance  float64
}

// clusterCorpus reads the whole embedded corpus, clusters it, and writes the
// cluster id and importance of every chunk back. It returns without writing when
// the corpus has no embedded chunks yet (a fresh database before any embed run),
// so the job is safe to run at any time. It logs the start, the cluster-count
// and importance distribution, so a long or degenerate run is observable.
func clusterCorpus(ctx context.Context, logger *slog.Logger, store clusterStore, cfg config.WikiCluster) (stats, error) {
	chunks, err := readEmbedded(ctx, store, cfg.ReadBatch)
	if err != nil {
		return stats{}, err
	}
	if len(chunks) == 0 {
		logger.InfoContext(ctx, "no embedded chunks to cluster; run the embedding pipeline first")
		return stats{}, nil
	}
	logger.InfoContext(ctx, "clustering embedded corpus",
		slog.Int("chunks", len(chunks)), slog.Int("k", cfg.K))

	vectors := make([][]float32, len(chunks))
	for i, c := range chunks {
		vectors[i] = c.Embedding
	}
	assignments, err := cluster.Cluster(vectors, cluster.Config{K: cfg.K, MaxIters: cfg.MaxIters, Seed: cfg.Seed})
	if err != nil {
		return stats{}, fmt.Errorf("wikicluster: cluster corpus: %w", err)
	}

	scored := make([]domain.EvidenceChunk, len(chunks))
	for i := range chunks {
		clusterID := assignments[i].Cluster
		importance := assignments[i].Importance
		scored[i] = domain.EvidenceChunk{
			Source:     chunks[i].Source,
			ExternalID: chunks[i].ExternalID,
			ChunkIndex: chunks[i].ChunkIndex,
			Metadata:   domain.WikiMetadata{ClusterID: &clusterID, Importance: &importance}.Map(),
		}
	}
	if err := writeClustering(ctx, store, scored, cfg.WriteBatch); err != nil {
		return stats{}, err
	}

	st := summarize(assignments)
	logger.InfoContext(ctx, "clustered embedded corpus",
		slog.Int("chunks", st.Chunks),
		slog.Int("clusters", st.Clusters),
		slog.Int("largest_cluster", st.LargestCluster),
		slog.Float64("importance_min", st.MinImportance),
		slog.Float64("importance_mean", st.MeanImportance),
		slog.Float64("importance_max", st.MaxImportance))
	return st, nil
}

// readEmbedded pages the whole embedded corpus into memory in keyset order. The
// corpus must fit in memory to be clustered in one pass; for a simplewiki-sized
// corpus that is well within a worker's footprint (a larger corpus needs a
// streaming or mini-batch variant, noted as future work).
func readEmbedded(ctx context.Context, store clusterStore, batch int) ([]domain.EvidenceChunk, error) {
	var (
		all []domain.EvidenceChunk
		cur domain.EvidenceCursor
	)
	for {
		page, err := store.EmbeddedChunks(ctx, cur, batch)
		if err != nil {
			return nil, fmt.Errorf("wikicluster: read embedded chunks: %w", err)
		}
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
		last := page[len(page)-1]
		cur = domain.EvidenceCursor{Source: last.Source, ExternalID: last.ExternalID, ChunkIndex: int32(last.ChunkIndex)}
		if len(page) < batch {
			break
		}
	}
	return all, nil
}

// writeClustering writes the scored chunks back in batch-sized groups, so one
// huge SendBatch does not pin the whole corpus in a single round-trip.
func writeClustering(ctx context.Context, store clusterStore, scored []domain.EvidenceChunk, batch int) error {
	for start := 0; start < len(scored); start += batch {
		end := min(start+batch, len(scored))
		if err := store.SetChunkClustering(ctx, scored[start:end]); err != nil {
			return fmt.Errorf("wikicluster: write clustering: %w", err)
		}
	}
	return nil
}

// summarize reduces the assignments to the run's observable stats.
func summarize(assignments []cluster.Assignment) stats {
	sizes := map[int32]int{}
	minImp, maxImp, sum := math.Inf(1), math.Inf(-1), 0.0
	for _, a := range assignments {
		sizes[a.Cluster]++
		minImp = min(minImp, a.Importance)
		maxImp = max(maxImp, a.Importance)
		sum += a.Importance
	}
	largest := 0
	for _, s := range sizes {
		largest = max(largest, s)
	}
	return stats{
		Chunks:         len(assignments),
		Clusters:       len(sizes),
		LargestCluster: largest,
		MinImportance:  minImp,
		MeanImportance: sum / float64(len(assignments)),
		MaxImportance:  maxImp,
	}
}
