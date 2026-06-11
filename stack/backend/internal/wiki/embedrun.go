package wiki

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// charsPerToken is Voyage's documented average for estimating token counts
// from character counts without calling the tokenizer (docs.voyageai.com,
// verified 2026-06). It only feeds the dry-run cost estimate, never billing.
const charsPerToken = 5

// pricePerMTokenUSD is the voyage-4-large price per one million tokens
// (docs.voyageai.com, verified 2026-06: $0.12/M, first 200M tokens free).
const pricePerMTokenUSD = 0.12

// BulkPlan is what a bulk run should do for the dump version it resolved,
// decided by the store from the staging table and the live checkpoint.
type BulkPlan int

const (
	// PlanBuild rebuilds staging from the dump, then embeds and swaps. It is the
	// default: a fresh corpus, an interrupted build, or a staging left from a
	// different dump all fall here.
	PlanBuild BulkPlan = iota
	// PlanResumeEmbed keeps a staging already materialized for this dump and
	// embeds only its remaining chunks before swapping.
	PlanResumeEmbed
	// PlanAlreadyCurrent means the live corpus already serves this dump fully
	// embedded; the run is a no-op.
	PlanAlreadyCurrent
)

// Estimate is the dry-run projection of a bulk-embedding run.
type Estimate struct {
	Pages   int64
	Chunks  int64
	Tokens  int64
	CostUSD float64
}

// EmbedStats summarizes a completed bulk-embedding run.
type EmbedStats struct {
	Embedded int
}

// Config tunes the bulk-embedding run. BatchSize is the number of chunks per
// Voyage request (bounded by the API's per-request limits); Concurrency is how
// many requests run at once, capping load against the rate limit.
// MaintenanceWorkMem and MaxParallelWorkers tune the post-load HNSW index
// build; the pipeline forwards them to the store without interpreting them.
type Config struct {
	Corpus             string
	DumpVersion        string
	BatchSize          int
	Concurrency        int
	MaintenanceWorkMem string
	MaxParallelWorkers int
}

// Embedder embeds documents for storage (input_type=document). The bulk
// pipeline depends only on this narrow surface, so the live Voyage client, a
// retrying decorator, or a fake all satisfy it.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbedSource is the read side the dry-run estimate needs: how many staging
// chunks remain to embed. The estimate depends on this alone, so it never
// touches the write side.
type EmbedSource interface {
	// StagingRemaining counts the staging chunks, pages, and characters still to
	// embed.
	StagingRemaining(ctx context.Context) (domain.WikiRemaining, error)
}

// BulkStore is the full store surface a bulk run drives: the embed loop reads
// the pending staging chunks and writes their embeddings in place, then
// finalize indexes the staging table and swaps it atomically into wiki_chunks,
// checkpointing the corpus at the dump version.
type BulkStore interface {
	EmbedSource
	// UnembeddedStaging returns up to limit staging chunks lacking an embedding,
	// in keyset order. The next run reads whatever an interrupted one left.
	UnembeddedStaging(ctx context.Context, limit int) ([]domain.WikiChunk, error)
	// UpdateStagingEmbeddings writes embeddings onto existing staging rows.
	UpdateStagingEmbeddings(ctx context.Context, chunks []domain.WikiChunk) error
	// FinalizeStaging indexes staging and swaps it into wiki_chunks, checkpointing
	// the corpus at version with lastChangeTS. The build settings tune the HNSW
	// index build session.
	FinalizeStaging(ctx context.Context, corpus, version string, lastChangeTS time.Time, maintenanceWorkMem string, maxParallelWorkers int) error
}

// EstimateBulkEmbed projects the cost of embedding the staging chunks still
// pending, without calling the embedding API. On a freshly built staging this is
// the whole corpus; on a resumed one it is only what is left to embed.
func EstimateBulkEmbed(ctx context.Context, src EmbedSource) (Estimate, error) {
	rem, err := src.StagingRemaining(ctx)
	if err != nil {
		return Estimate{}, fmt.Errorf("wiki: staging remaining: %w", err)
	}
	tokens := rem.Chars / charsPerToken
	return Estimate{
		Pages:   rem.Pages,
		Chunks:  rem.Chunks,
		Tokens:  tokens,
		CostUSD: float64(tokens) / 1e6 * pricePerMTokenUSD,
	}, nil
}

// RunBulkEmbed embeds every staging chunk still lacking an embedding and swaps
// the result into place. It embeds in keyset order and writes each batch back
// onto staging before reading the next, so a crash leaves the embedded rows
// committed and the next run resumes past them; only once nothing remains does
// it index and swap atomically. It logs the pending total at the start, one line
// per embedded HTTP batch, and the finalize step, so a long or stalled run is
// observable; a nil logger falls back to slog.Default.
func RunBulkEmbed(ctx context.Context, logger *slog.Logger, store BulkStore, embedder Embedder, cfg Config) (EmbedStats, error) {
	if cfg.BatchSize < 1 || cfg.Concurrency < 1 {
		return EmbedStats{}, fmt.Errorf("wiki: bulk embed needs positive batch size and concurrency, got %d and %d", cfg.BatchSize, cfg.Concurrency)
	}
	if logger == nil {
		logger = slog.Default()
	}

	rem, err := store.StagingRemaining(ctx)
	if err != nil {
		return EmbedStats{}, fmt.Errorf("wiki: staging remaining: %w", err)
	}
	logger.InfoContext(ctx, "starting bulk embed",
		slog.String("corpus", cfg.Corpus),
		slog.Int64("pending_chunks", rem.Chunks))

	var (
		stats      EmbedStats
		embedded64 atomic.Int64
	)
	superBatch := cfg.BatchSize * cfg.Concurrency
	for {
		chunks, err := store.UnembeddedStaging(ctx, superBatch)
		if err != nil {
			return EmbedStats{}, fmt.Errorf("wiki: read pending staging chunks: %w", err)
		}
		if len(chunks) == 0 {
			break
		}
		embedded, err := embedChunks(ctx, logger, embedder, chunks, cfg, &embedded64, rem.Chunks)
		if err != nil {
			return EmbedStats{}, err
		}
		if err := store.UpdateStagingEmbeddings(ctx, embedded); err != nil {
			return EmbedStats{}, fmt.Errorf("wiki: write staging embeddings: %w", err)
		}
		stats.Embedded += len(embedded)
	}

	logger.InfoContext(ctx, "all pending chunks embedded; building index and swapping staging into wiki_chunks",
		slog.String("corpus", cfg.Corpus),
		slog.Int("embedded_this_run", stats.Embedded))
	if err := store.FinalizeStaging(ctx, cfg.Corpus, cfg.DumpVersion, parseDumpTime(cfg.DumpVersion), cfg.MaintenanceWorkMem, cfg.MaxParallelWorkers); err != nil {
		return EmbedStats{}, fmt.Errorf("wiki: finalize staging: %w", err)
	}
	logger.InfoContext(ctx, "bulk embed finalized; wiki_chunks now serves the embedded corpus",
		slog.String("corpus", cfg.Corpus))
	return stats, nil
}

// embedChunks embeds a super-batch by splitting it into BatchSize requests run
// up to Concurrency at a time, writing each result back onto its chunk so the
// returned slice stays in the input's keyset order regardless of completion
// order. It emits one log line per completed HTTP batch carrying the batch's
// embed latency (embed_duration spans the whole call, so on a first-try success
// it is the single request's latency, and on a retried one it also includes the
// backoff waits), advancing the shared done counter so the line also reports the
// run's cumulative progress against total. The latency makes a slow provider
// visible: when it approaches the embed HTTP timeout, lower WIKI_EMBED_BATCH_SIZE
// or WIKI_EMBED_CONCURRENCY, or raise WIKI_EMBED_HTTP_TIMEOUT.
// A negative total means the count is unknown (the delta path embeds in place
// without a pending count), and the line omits pending_total rather than
// reporting a misleading zero.
func embedChunks(ctx context.Context, logger *slog.Logger, embedder Embedder, chunks []domain.WikiChunk, cfg Config, done *atomic.Int64, total int64) ([]domain.WikiChunk, error) {
	out := make([]domain.WikiChunk, len(chunks))
	copy(out, chunks)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)
	for start := 0; start < len(chunks); start += cfg.BatchSize {
		end := min(start+cfg.BatchSize, len(chunks))
		g.Go(func() error {
			texts := make([]string, end-start)
			for i := start; i < end; i++ {
				texts[i-start] = chunks[i].Content
			}
			embedStart := time.Now()
			embeddings, err := embedder.EmbedDocuments(gctx, texts)
			if err != nil {
				return fmt.Errorf("wiki: embed chunks [%d:%d]: %w", start, end, err)
			}
			embedDuration := time.Since(embedStart)
			if len(embeddings) != end-start {
				return fmt.Errorf("wiki: embed chunks [%d:%d]: got %d embeddings, want %d", start, end, len(embeddings), end-start)
			}
			for i := start; i < end; i++ {
				out[i].Embedding = embeddings[i-start]
			}
			attrs := []slog.Attr{
				slog.Int("batch_chunks", end-start),
				slog.Int64("embedded", done.Add(int64(end-start))),
				slog.Duration("embed_duration", embedDuration),
				slog.Int64("through_page", chunks[end-1].PageID),
				slog.String("through_title", chunks[end-1].Title),
			}
			// A negative total signals an unknown pending count (the delta path);
			// only report pending_total when the caller knows it.
			if total >= 0 {
				attrs = append(attrs, slog.Int64("pending_total", total))
			}
			logger.LogAttrs(gctx, slog.LevelInfo, "embedded wiki chunk batch", attrs...)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
