// Command wikiverify checks that the live wiki corpus is fully and correctly
// rebuilt: it has chunks, every chunk carries a non-null, non-zero,
// 1024-dimension embedding, the per-chunk metadata is populated, and the HNSW
// index is present and valid. It is the verification step of the full reingest:
// it logs each check with concrete counts and exits non-zero the moment any
// check fails, so an incomplete or stale rebuild is caught loudly rather than
// served. The database comes from DATABASE_URL.
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

	health, err := store.WikiCorpusHealth(ctx)
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
func evaluate(h domain.WikiCorpusHealth) []check {
	return []check{
		{"chunks present", h.Chunks > 0, fmt.Sprintf("%d chunks", h.Chunks)},
		{"all chunks embedded", h.NullEmbeddings == 0, fmt.Sprintf("%d chunks with a null embedding", h.NullEmbeddings)},
		{"no zero-vector embeddings", h.ZeroVectors == 0, fmt.Sprintf("%d zero-vector embeddings", h.ZeroVectors)},
		{"embedding dimension 1024", h.EmbeddingType == "halfvec(1024)", fmt.Sprintf("embedding column is %q", h.EmbeddingType)},
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
func report(ctx context.Context, logger *slog.Logger, h domain.WikiCorpusHealth) error {
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
