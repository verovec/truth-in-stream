package stats

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// StatCorpus is the corpus label stamped on EU (Eurostat) statistical evidence
// rows. It is distinct from the Wikipedia corpus so the provenance of a
// retrieved passage is identifiable, while both share the evidence_chunks table and
// the single SearchWiki retrieval path the fact-check verifier already uses. It
// aliases domain.StatCorpus, one of the shared constants the store filters on.
const StatCorpus = domain.StatCorpus

// defaultUpsertBatchSize bounds how many rendered passages are upserted per
// statement. A modest batch keeps each write cheap while still amortizing the
// round-trip over many rows when a broad sweep yields thousands of passages.
const defaultUpsertBatchSize = 128

// Source yields the statistical datapoints to ingest under its own corpus
// label. The EU SDMX adapter (subpackage eurostat) is the first implementation;
// the foundation is source-agnostic so further national sources reuse it
// unchanged, each stamping a distinct corpus (domain.StatCorpora) so a retrieved
// passage's publisher is identifiable and the wiki-only maintenance reads can
// exclude every statistical corpus.
type Source interface {
	Datapoints(ctx context.Context) ([]domain.Datapoint, error)
	// Corpus is the evidence_chunks.source label every passage from this source is
	// stamped with; it must be one of domain.StatCorpora.
	Corpus() string
}

// Store writes rendered passages into the live evidence corpus without an
// embedding and pages back the still-unembedded ones for the producer to
// enqueue. The chunks are upserted on their (page_id, chunk_index) provenance
// key (UpsertChunks keeps an unchanged chunk's existing vector and clears it only
// where content changed), so a re-run refreshes the figures without duplicating
// passages and never strands a stale vector. The corpus-scoped reads page only
// this source's un-embedded rows, so a stats run never re-publishes another
// corpus's pending chunks. *postgres.Store satisfies it.
//
// Unlike the Wikipedia bulk path, the stats path does NOT claim the wiki corpus
// lock: the statistical corpora deliberately coexist with the encyclopedic corpus
// in evidence_chunks (each is excluded from the wiki-only maintenance reads), so
// EnsureSource's single-source invariant - a Wikipedia-rebuild concern - must not
// gate them.
type Store interface {
	UpsertChunks(ctx context.Context, chunks []domain.EvidenceChunk) error
	CountUnembeddedLiveSource(ctx context.Context, corpus string) (int64, error)
	UnembeddedLiveSource(ctx context.Context, corpus string, after domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error)
}

// Publisher enqueues an embedding-job body at a priority for the worker fleet,
// the same narrow surface the wiki producer depends on. cmd/statsingest adapts
// the RabbitMQ client to it, so the stats package never imports the transport.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config tunes a stats ingest run. UpsertBatchSize bounds how many rendered
// passages are upserted per statement (a non-positive value takes the default);
// MaxPriority and EnqueueBatchSize are forwarded to the embed-job producer (the
// queue's priority ceiling and the publish page size).
type Config struct {
	UpsertBatchSize  int
	MaxPriority      uint8
	EnqueueBatchSize int
}

// Stats summarizes a completed ingest run for one source.
type Stats struct {
	// Upserted is how many rendered passages were written into the live corpus.
	Upserted int
	// Published is how many embedding jobs were enqueued for the fleet (the
	// still-unembedded passages, so a re-run publishes only what is pending).
	Published int
}

// Run pulls datapoints from the source, renders each to a self-contained French
// evidence sentence, upserts every passage into the live corpus un-embedded with
// provenance, then publishes one prioritized embedding job per still-unembedded
// passage to the worker fleet - the same bulk-into-live pattern the Wikipedia
// corpus uses. It does no inline embedding: the fleet fills the vectors in place
// on the live HNSW index, so the corpus grows monotonically and a broad sweep
// scales by worker replica count rather than one synchronous Voyage burst.
//
// Idempotency comes from the stable (SeriesPageID, PeriodChunkIndex) provenance
// key plus an upsert, so a scheduled refresh never duplicates a datapoint+period;
// the producer enqueues only un-embedded rows (publishing is at-least-once with
// an idempotent keyed worker write), so a re-run re-publishes nothing already
// embedded. Errors at every stage are wrapped with %w so a provider or store
// failure is distinguishable upstream. A nil logger falls back to slog.Default.
func Run(ctx context.Context, logger *slog.Logger, src Source, store Store, pub Publisher, cfg Config) (Stats, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.UpsertBatchSize <= 0 {
		cfg.UpsertBatchSize = defaultUpsertBatchSize
	}

	corpus := src.Corpus()
	if !domain.IsStatCorpus(corpus) {
		return Stats{}, fmt.Errorf("stats: source corpus %q is not a registered statistical corpus", corpus)
	}

	datapoints, err := src.Datapoints(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("stats: fetch datapoints: %w", err)
	}

	chunks, err := renderChunks(datapoints, corpus)
	if err != nil {
		return Stats{}, err
	}

	upserted, err := upsertUnembedded(ctx, store, chunks, cfg.UpsertBatchSize)
	if err != nil {
		return Stats{}, err
	}
	logger.InfoContext(ctx, "statistical passages upserted un-embedded into the live corpus",
		slog.String("corpus", corpus), slog.Int("upserted", upserted))

	enqueue, err := wiki.RunBulkLivePublish(ctx, logger, corpusLiveStore{store: store, corpus: corpus}, pub, wiki.ProducerConfig{
		Corpus:           corpus,
		MaxPriority:      cfg.MaxPriority,
		EnqueueBatchSize: cfg.EnqueueBatchSize,
	})
	if err != nil {
		return Stats{}, fmt.Errorf("stats: publish embedding jobs: %w", err)
	}

	return Stats{Upserted: upserted, Published: enqueue.Published}, nil
}

// renderChunks renders each datapoint into a French evidence passage keyed on its
// (series, period) provenance, validating the datapoint and deriving the
// (page_id, chunk_index) embed-job key before any write. It fails fast on the
// first invalid datapoint so bad source data never reaches the corpus. Two
// datapoints that map to the same provenance key (a source that lists a
// series+period twice) collapse to the last occurrence, so the upserted count
// matches the rows actually written and one key never reaches the store twice in
// a batch - mirroring the store's idempotent upsert on that same key.
func renderChunks(datapoints []domain.Datapoint, corpus string) ([]domain.EvidenceChunk, error) {
	chunks := make([]domain.EvidenceChunk, 0, len(datapoints))
	index := make(map[domain.EvidenceCursor]int, len(datapoints))
	for _, d := range datapoints {
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("stats: invalid datapoint (%s %s %s): %w", d.Dataset, d.SeriesKey, d.Period, err)
		}
		chunkIndex, err := d.PeriodChunkIndex()
		if err != nil {
			return nil, fmt.Errorf("stats: chunk index for %s %s: %w", d.Dataset, d.Period, err)
		}
		pageID := d.SeriesPageID()
		if pageID <= 0 {
			// SeriesPageID masks the sign bit, so it is non-negative; a zero is the
			// vanishingly rare hash-to-zero case the embed-job validator would reject.
			// Fail loudly here rather than enqueue a job the fleet silently drops.
			return nil, fmt.Errorf("stats: datapoint (%s %s) derived a non-positive page id %d", d.Dataset, d.SeriesKey, pageID)
		}
		externalID := strconv.FormatInt(pageID, 10)
		// A statistical passage carries no wiki provenance (no revision or
		// section), so its metadata is empty - the source-extensible schema in
		// action: a new source is rows under a new source value with its own
		// (here absent) metadata keys, no column and no migration.
		chunk := domain.EvidenceChunk{
			Source:     corpus,
			ExternalID: externalID,
			ChunkIndex: chunkIndex,
			Title:      d.Title,
			URL:        d.SourceURL,
			Content:    RenderFrench(d),
			Kind:       domain.EvidenceKindLead,
		}
		k := domain.EvidenceCursor{Source: corpus, ExternalID: externalID, ChunkIndex: int32(chunkIndex)}
		if i, dup := index[k]; dup {
			chunks[i] = chunk
			continue
		}
		index[k] = len(chunks)
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

// upsertUnembedded writes the rendered passages into the live corpus in batches,
// each on its (page_id, chunk_index) provenance key, and returns the count
// written. The upsert leaves the embedding NULL (or clears a stale one when the
// content changed), so the fleet fills it; an unchanged re-run keeps the prior
// vector untouched.
func upsertUnembedded(ctx context.Context, store Store, chunks []domain.EvidenceChunk, batchSize int) (int, error) {
	total := 0
	for start := 0; start < len(chunks); start += batchSize {
		end := min(start+batchSize, len(chunks))
		batch := chunks[start:end]
		if err := store.UpsertChunks(ctx, batch); err != nil {
			return total, fmt.Errorf("stats: upsert passages [%d:%d]: %w", start, end, err)
		}
		total += len(batch)
	}
	return total, nil
}

// corpusLiveStore adapts a corpus-scoped Store to wiki.LiveProducerStore so the
// shared bulk-into-live producer pages and counts only this source's un-embedded
// rows. Reusing the wiki producer keeps the at-least-once, keyset-resume publish
// identical across corpora; the scoping is the only stats-specific concern, so it
// lives here rather than forking the producer.
type corpusLiveStore struct {
	store  Store
	corpus string
}

func (c corpusLiveStore) CountUnembeddedLive(ctx context.Context) (int64, error) {
	return c.store.CountUnembeddedLiveSource(ctx, c.corpus)
}

func (c corpusLiveStore) UnembeddedLive(ctx context.Context, after domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	return c.store.UnembeddedLiveSource(ctx, c.corpus, after, limit)
}
