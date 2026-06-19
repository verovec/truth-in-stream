package stats

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// StatCorpus is the corpus label stamped on statistical evidence rows. It is
// distinct from the Wikipedia corpus so the provenance of a retrieved passage
// is identifiable, while both share the wiki_chunks table and the single
// SearchWiki retrieval path the fact-check verifier already uses. It aliases
// domain.StatCorpus, the shared constant the store filters on.
const StatCorpus = domain.StatCorpus

// defaultBatchSize bounds how many datapoints are embedded per request. Voyage
// allows up to 1000 inputs; 128 keeps requests modest, matching the claims
// ingest default.
const defaultBatchSize = 128

// Source yields the statistical datapoints to ingest. The EU SDMX adapter
// (subpackage eurostat) is the first implementation; the foundation is
// source-agnostic so further sources reuse it unchanged.
type Source interface {
	Datapoints(ctx context.Context) ([]domain.Datapoint, error)
}

// Embedder embeds rendered passages for storage (input_type=document), the same
// document-embedding contract the claims and wiki corpora use.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// Store writes one embedded passage atomically under its (page_id, chunk_index)
// provenance key. UpsertEmbeddedChunk is the wiki store's single-statement
// text-form ::halfvec write (never binary COPY), so a re-run rewrites the same
// row idempotently and a row is never searchable without its vector.
type Store interface {
	UpsertEmbeddedChunk(ctx context.Context, chunk domain.WikiChunk) error
}

// Run pulls datapoints from the source, renders each to a French evidence
// sentence, embeds the batch, and upserts every passage with provenance. It
// returns the number of passages written. Idempotency comes from the stable
// (SeriesPageID, PeriodChunkIndex) key plus an upsert, so a scheduled refresh
// never duplicates a datapoint+period. Errors at every stage are wrapped with
// %w so a provider or store failure is distinguishable upstream.
func Run(ctx context.Context, src Source, embedder Embedder, store Store, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	datapoints, err := src.Datapoints(ctx)
	if err != nil {
		return 0, fmt.Errorf("stats: fetch datapoints: %w", err)
	}

	chunks := make([]domain.WikiChunk, 0, len(datapoints))
	for _, d := range datapoints {
		if err := d.Validate(); err != nil {
			return 0, fmt.Errorf("stats: invalid datapoint (%s %s %s): %w", d.Dataset, d.SeriesKey, d.Period, err)
		}
		chunkIndex, err := d.PeriodChunkIndex()
		if err != nil {
			return 0, fmt.Errorf("stats: chunk index for %s %s: %w", d.Dataset, d.Period, err)
		}
		chunks = append(chunks, domain.WikiChunk{
			PageID:     d.SeriesPageID(),
			ChunkIndex: chunkIndex,
			Title:      d.Title,
			URL:        d.SourceURL,
			RevisionID: 0,
			Corpus:     StatCorpus,
			Content:    RenderFrench(d),
			Section:    "",
			Kind:       domain.WikiChunkKindLead,
		})
	}

	total := 0
	for start := 0; start < len(chunks); start += batchSize {
		end := min(start+batchSize, len(chunks))
		batch := chunks[start:end]

		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}
		embeddings, err := embedder.EmbedDocuments(ctx, texts)
		if err != nil {
			return total, fmt.Errorf("stats: embed batch [%d:%d]: %w", start, end, err)
		}
		if len(embeddings) != len(batch) {
			return total, fmt.Errorf("stats: embed batch [%d:%d]: got %d embeddings, want %d", start, end, len(embeddings), len(batch))
		}

		for i := range batch {
			batch[i].Embedding = embeddings[i]
			if err := store.UpsertEmbeddedChunk(ctx, batch[i]); err != nil {
				return total, fmt.Errorf("stats: upsert %s/%d: %w", batch[i].Title, batch[i].ChunkIndex, err)
			}
			total++
		}
	}
	return total, nil
}
