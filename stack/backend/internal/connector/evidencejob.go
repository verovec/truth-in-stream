package connector

import (
	"fmt"
	"math"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// EvidenceJob is the generic, self-contained unit of evidence-ingest work a
// connector publishes for the evidence_chunks corpus. Every field a
// [domain.EvidenceChunk] row needs travels in the body, so the consuming worker
// embeds and upserts the chunk without a prior database read. It is the
// source-neutral generalisation of the wiki crawl job: identity is the natural
// key (Source, ExternalID, ChunkIndex), and any source-specific provenance lives
// in Metadata (stored verbatim as jsonb) rather than in a new column, so a new
// source is rows under a new Source value - never a migration.
//
// Attempt is the delivery attempt so far: a producer leaves it zero and a worker
// increments it on a transient-failure re-enqueue, so a job that keeps failing is
// eventually dead-lettered instead of looping forever - the same retry contract
// the crawl worker already applies.
type EvidenceJob struct {
	Source     string         `json:"source"`
	ExternalID string         `json:"external_id"`
	ChunkIndex int            `json:"chunk_index"`
	Title      string         `json:"title"`
	URL        string         `json:"url"`
	Content    string         `json:"content"`
	Kind       string         `json:"kind"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	// PublishedAt is the source's own publication date for the document, nil
	// when the source is undated. Additive on the queue wire: an old message
	// without the field decodes to nil and stores an undated chunk.
	PublishedAt *time.Time `json:"published_at,omitempty"`
	Attempt     int        `json:"attempt,omitzero"`
}

// Validate rejects a job that can never produce a valid corpus row, so a worker
// dead-letters it instead of embedding nonsense or looping forever. It mirrors
// the invariants the store enforces on write (a non-empty source, external id,
// and content; a non-negative in-range chunk index; a known lead/body kind), so a
// job that validates here is one the store will accept.
func (j EvidenceJob) Validate() error {
	switch {
	case j.Source == "":
		return fmt.Errorf("evidence job: empty source")
	case j.ExternalID == "":
		return fmt.Errorf("evidence job %q: empty external id", j.Source)
	case j.ChunkIndex < 0:
		return fmt.Errorf("evidence job %s/%s: chunk index %d must not be negative", j.Source, j.ExternalID, j.ChunkIndex)
	case j.ChunkIndex > math.MaxInt32:
		return fmt.Errorf("evidence job %s/%s: chunk index %d exceeds the column width", j.Source, j.ExternalID, j.ChunkIndex)
	case j.Content == "":
		return fmt.Errorf("evidence job %s/%s chunk %d: empty content", j.Source, j.ExternalID, j.ChunkIndex)
	case !domain.EvidenceChunkKind(j.Kind).Valid():
		return fmt.Errorf("evidence job %s/%s chunk %d: invalid kind %q", j.Source, j.ExternalID, j.ChunkIndex, j.Kind)
	case j.Attempt < 0:
		return fmt.Errorf("evidence job %s/%s chunk %d: negative attempt %d", j.Source, j.ExternalID, j.ChunkIndex, j.Attempt)
	default:
		return nil
	}
}

// Chunk renders the job as the un-embedded [domain.EvidenceChunk] the embedding
// pipeline stores: the natural key and text pass through unchanged, Metadata is
// carried verbatim as the jsonb payload, and Embedding stays nil for the worker
// to fill from Content. Call Validate first; Chunk does not re-validate.
func (j EvidenceJob) Chunk() domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:      j.Source,
		ExternalID:  j.ExternalID,
		ChunkIndex:  j.ChunkIndex,
		Title:       j.Title,
		URL:         j.URL,
		Content:     j.Content,
		Kind:        domain.EvidenceChunkKind(j.Kind),
		Metadata:    j.Metadata,
		PublishedAt: j.PublishedAt,
		Embedding:   nil,
	}
}
