// Command wikiverify checks that the live wiki corpus is correct and reports how
// far its embedding has progressed. It gates on consistency over the embedded
// rows - it has chunks, no embedded chunk is a zero vector, the embedding column
// is exactly halfvec(1024), the per-chunk metadata is populated, and the HNSW
// index is present and valid - and reports embedded coverage as progress rather
// than failing on it. With bulk-into-live ingestion the corpus is usable and
// correct while it fills in, so "100% embedded" is no longer a usability gate; a
// real defect (a zero vector, a wrong dimension, a missing index) still exits
// non-zero so a broken corpus is caught loudly rather than served. The database
// comes from DATABASE_URL.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("wiki corpus verification failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
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

	health, err := store.EvidenceCorpusHealth(ctx)
	if err != nil {
		return err
	}
	return report(ctx, logger, health)
}

// check is one named corpus invariant and whether it held, with a human-readable
// detail (a count or value) for the log.
type check struct {
	name   string
	ok     bool
	detail string
}

// evaluate turns a corpus health snapshot into the ordered list of checks the
// verifier reports. It is pure so the pass/fail decision is unit-tested without a
// database.
func evaluate(h domain.EvidenceCorpusHealth) []check {
	embedded := h.Chunks - h.NullEmbeddings
	coverage := 0.0
	if h.Chunks > 0 {
		coverage = 100 * float64(embedded) / float64(h.Chunks)
	}
	return []check{
		{"chunks present", h.Chunks > 0, fmt.Sprintf("%d chunks", h.Chunks)},
		// Embedded coverage is progress, never a gate: a bulk-into-live corpus is
		// usable and correct while the fleet fills it in, so verification reports how
		// far along it is rather than requiring 100% embedded. ok is always true.
		{"embedded coverage", true, fmt.Sprintf("%d/%d chunks embedded (%.1f%%)", embedded, h.Chunks, coverage)},
		// The consistency gates below scope to the embedded rows (the zero-vector
		// check filters out NULLs; the dimension is the column type that binds every
		// row), so a partially embedded corpus passes as long as what is embedded is
		// sound.
		{"no zero-vector embeddings", h.ZeroVectors == 0, fmt.Sprintf("%d zero-vector embeddings", h.ZeroVectors)},
		{fmt.Sprintf("embedding dimension %d", domain.EmbeddingDim), h.EmbeddingType == domain.HalfvecColumnType(), fmt.Sprintf("embedding column is %q", h.EmbeddingType)},
		{"per-chunk metadata populated", h.MissingMetadata == 0, fmt.Sprintf("%d chunks with an invalid kind", h.MissingMetadata)},
		{"HNSW index present", h.HNSWPresent, fmt.Sprintf("present=%t", h.HNSWPresent)},
		{"HNSW index valid", h.HNSWValid, fmt.Sprintf("valid=%t", h.HNSWValid)},
	}
}

// passed reports whether every check held.
func passed(checks []check) bool {
	for _, c := range checks {
		if !c.ok {
			return false
		}
	}
	return true
}

// report logs each check (a failed one at ERROR so it stands out) and returns an
// error when any failed, which the caller turns into a non-zero exit.
func report(ctx context.Context, logger *slog.Logger, h domain.EvidenceCorpusHealth) error {
	checks := evaluate(h)
	for _, c := range checks {
		level := slog.LevelInfo
		if !c.ok {
			level = slog.LevelError
		}
		logger.LogAttrs(ctx, level, "corpus check",
			slog.String("check", c.name),
			slog.Bool("ok", c.ok),
			slog.String("detail", c.detail))
	}
	if !passed(checks) {
		return errors.New("wikiverify: corpus verification failed; the corpus is incomplete or stale - re-run the full reingest")
	}
	logger.InfoContext(ctx, "corpus verification passed",
		slog.Int64("chunks", h.Chunks),
		slog.String("embedding_type", h.EmbeddingType))
	return nil
}
