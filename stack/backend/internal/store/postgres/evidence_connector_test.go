package postgres

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// TestEvidenceJobRoundTripsIntoCorpus is the AC4 guard: a generic
// connector.EvidenceJob a non-wiki source publishes maps through EvidenceJob.Chunk
// and the same UpsertEmbeddedChunk write the crawl worker uses, landing an
// evidence_chunks row under the source's own Source value with its metadata
// carried verbatim. It skips cleanly without a database, so it runs in the same
// integration lane as the other store tests.
func TestEvidenceJobRoundTripsIntoCorpus(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	job := connector.EvidenceJob{
		Source:     "insee-series",
		ExternalID: "001688370",
		ChunkIndex: 0,
		Title:      "Taux de chomage",
		URL:        "https://www.insee.fr/fr/statistiques/serie/001688370",
		Content:    "Le taux de chomage au sens du BIT s'etablit a 7,4 % au T1 2024.",
		Kind:       string(domain.EvidenceKindLead),
		Metadata:   map[string]any{"idbank": "001688370", "period": "2024-Q1"},
	}
	if err := job.Validate(); err != nil {
		t.Fatalf("job.Validate: %v", err)
	}

	// The consuming worker fills the embedding from the content; simulate that so
	// the write exercises the real store path.
	chunk := job.Chunk()
	chunk.Embedding = fullEmbedding()
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		t.Fatalf("UpsertEmbeddedChunk: %v", err)
	}

	got, err := store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{
		Source: job.Source, ExternalID: job.ExternalID, ChunkIndex: int32(job.ChunkIndex),
	})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	if got.Source != job.Source || got.Content != job.Content || got.Title != job.Title || got.Url != job.URL {
		t.Errorf("row = %s/%q/%q/%q, want %s/%q/%q/%q", got.Source, got.Content, got.Title, got.Url, job.Source, job.Content, job.Title, job.URL)
	}
	if got.Kind != string(domain.EvidenceKindLead) {
		t.Errorf("kind = %q, want lead", got.Kind)
	}
	if got.EmbeddingIsNull {
		t.Error("embedding is null, want the stored vector")
	}
}
