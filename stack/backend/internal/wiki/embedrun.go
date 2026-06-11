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

// EmbedSource is the read side of the store the bulk-embedding run needs: where
// staging has progressed to, the chunks still to embed, and the dry-run counts.
// Dry-run depends on this alone, so it never touches the write side.
type EmbedSource interface {
	// EmbedWatermark returns the greatest (page_id, chunk_index) already loaded
	// into staging, or the zero cursor when staging is empty or absent.
	EmbedWatermark(ctx context.Context) (domain.WikiCursor, error)
	// UnembeddedChunks returns up to limit live chunks ordered after cur.
	UnembeddedChunks(ctx context.Context, cur domain.WikiCursor, limit int) ([]domain.WikiChunk, error)
	// EstimateRemaining counts the live pages, chunks, and characters after cur.
	EstimateRemaining(ctx context.Context, cur domain.WikiCursor) (domain.WikiRemaining, error)
}

// EmbedSink is the write side: build the staging table, load embedded chunks
// into it, then index and swap it into place.
type EmbedSink interface {
	// DiscardStagingIfStale drops a surviving staging table built for a different
	// dump than dumpVersion (or one carrying no stamp), reporting whether it did.
	// A matching staging table is kept so an interrupted run still resumes.
	DiscardStagingIfStale(ctx context.Context, dumpVersion string) (bool, error)
	// CreateStaging creates the unindexed staging table if it is absent,
	// preserving an existing one so an interrupted run resumes into it, and
	// stamps it with dumpVersion.
	CreateStaging(ctx context.Context, dumpVersion string) error
	// CopyStagingChunks bulk-loads embedded chunks into staging.
	CopyStagingChunks(ctx context.Context, chunks []domain.WikiChunk) error
	// FinalizeStaging indexes the loaded staging table and swaps it atomically
	// into the live wiki_chunks, checkpointing the corpus. The build settings
	// tune the HNSW index build session.
	FinalizeStaging(ctx context.Context, corpus, maintenanceWorkMem string, maxParallelWorkers int) error
}

// EmbedStore is the full store surface a real bulk-embedding run drives.
type EmbedStore interface {
	EmbedSource
	EmbedSink
}

// EstimateBulkEmbed projects the cost of embedding the chunks still pending,
// without calling the embedding API. On a fresh corpus this is the whole
// corpus; on a resumed run it is only what staging has not yet absorbed.
func EstimateBulkEmbed(ctx context.Context, src EmbedSource) (Estimate, error) {
	cur, err := src.EmbedWatermark(ctx)
	if err != nil {
		return Estimate{}, fmt.Errorf("wiki: read embed watermark: %w", err)
	}
	rem, err := src.EstimateRemaining(ctx, cur)
	if err != nil {
		return Estimate{}, fmt.Errorf("wiki: estimate remaining chunks: %w", err)
	}
	tokens := rem.Chars / charsPerToken
	return Estimate{
		Pages:   rem.Pages,
		Chunks:  rem.Chunks,
		Tokens:  tokens,
		CostUSD: float64(tokens) / 1e6 * pricePerMTokenUSD,
	}, nil
}

// RunBulkEmbed embeds every pending chunk and swaps the result into place. It
// loads embedded chunks into a staging table in keyset order, so a crash
// leaves staging a clean prefix that the next run resumes past; only once every
// pending chunk is loaded does it index and swap staging atomically. An empty
// corpus is never swapped. It logs the pending total at the start, one line per
// embedded HTTP batch, and the finalize step, so a long or stalled run is
// observable; a nil logger falls back to slog.Default.
func RunBulkEmbed(ctx context.Context, logger *slog.Logger, store EmbedStore, embedder Embedder, cfg Config) (EmbedStats, error) {
	if cfg.BatchSize < 1 || cfg.Concurrency < 1 {
		return EmbedStats{}, fmt.Errorf("wiki: bulk embed needs positive batch size and concurrency, got %d and %d", cfg.BatchSize, cfg.Concurrency)
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Drop a staging table left by an earlier run against a different dump before
	// reading the watermark: the freshly ingested corpus can hold chunks below
	// that staging's watermark which the keyset resume never revisits, so resuming
	// into it would strand them and the swap's staging==live guard would refuse
	// the corpus on every run. Discarding re-embeds from scratch against the
	// current dump; a staging table from the same dump is kept and resumed.
	discarded, err := store.DiscardStagingIfStale(ctx, cfg.DumpVersion)
	if err != nil {
		return EmbedStats{}, fmt.Errorf("wiki: discard stale staging: %w", err)
	}
	if discarded {
		logger.WarnContext(ctx, "discarded staging built for a different dump; re-embedding the corpus from scratch",
			slog.String("corpus", cfg.Corpus),
			slog.String("dump_version", cfg.DumpVersion))
	}

	start, err := store.EmbedWatermark(ctx)
	if err != nil {
		return EmbedStats{}, fmt.Errorf("wiki: read embed watermark: %w", err)
	}
	rem, err := store.EstimateRemaining(ctx, start)
	if err != nil {
		return EmbedStats{}, fmt.Errorf("wiki: estimate pending chunks: %w", err)
	}
	logger.InfoContext(ctx, "starting bulk embed",
		slog.String("corpus", cfg.Corpus),
		slog.Int64("pending_chunks", rem.Chunks),
		slog.Int64("resume_after_page", start.PageID))

	var (
		stats      EmbedStats
		embedded64 atomic.Int64
	)
	cur := start
	created := false
	superBatch := cfg.BatchSize * cfg.Concurrency
	for {
		chunks, err := store.UnembeddedChunks(ctx, cur, superBatch)
		if err != nil {
			return EmbedStats{}, fmt.Errorf("wiki: read pending chunks: %w", err)
		}
		if len(chunks) == 0 {
			break
		}
		// Create staging lazily, only once there is work, so a re-run on an
		// already-embedded corpus leaves no empty staging table behind.
		if !created {
			if err := store.CreateStaging(ctx, cfg.DumpVersion); err != nil {
				return EmbedStats{}, fmt.Errorf("wiki: create staging table: %w", err)
			}
			created = true
		}
		embedded, err := embedChunks(ctx, logger, embedder, chunks, cfg, &embedded64, rem.Chunks)
		if err != nil {
			return EmbedStats{}, err
		}
		if err := store.CopyStagingChunks(ctx, embedded); err != nil {
			return EmbedStats{}, fmt.Errorf("wiki: load embedded chunks: %w", err)
		}
		stats.Embedded += len(embedded)
		last := chunks[len(chunks)-1]
		cur = domain.WikiCursor{PageID: last.PageID, ChunkIndex: int32(last.ChunkIndex)}
	}

	// Finalize only when there is a staging table to swap: one filled this run,
	// or one a prior interrupted run left behind (start past the zero cursor).
	// Neither holds for an empty or already-embedded corpus, so it is a no-op.
	if !created && start == (domain.WikiCursor{}) {
		logger.InfoContext(ctx, "nothing to embed; corpus already current",
			slog.String("corpus", cfg.Corpus))
		return stats, nil
	}
	logger.InfoContext(ctx, "all pending chunks embedded; building index and swapping staging into wiki_chunks",
		slog.String("corpus", cfg.Corpus),
		slog.Int("embedded_this_run", stats.Embedded))
	if err := store.FinalizeStaging(ctx, cfg.Corpus, cfg.MaintenanceWorkMem, cfg.MaxParallelWorkers); err != nil {
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
