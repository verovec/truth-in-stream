package domain

import (
	"errors"
	"fmt"
	"time"
)

// ErrEvidenceSourceConflict reports that the store already holds a different
// encyclopedic corpus. The corpus is single-source per database (the delta
// sync's change-fraction denominator and page-id keys depend on it), so a
// claimant for another source must not proceed. Callers whose corpus work is
// optional (the dev seed) detect this with errors.Is and skip; the wiki sync
// itself treats it as fatal.
var ErrEvidenceSourceConflict = errors.New("evidence corpus already claimed by another source")

// EvidenceChunkKind classifies a chunk by the region of its source document it
// was extracted from. Ingestion extracts only lead sections today, so almost
// every chunk is EvidenceKindLead; the type and the evidence_chunks.kind column
// exist so body extraction and confidence weighting can tell lead from body
// prose. It stays a typed column (not metadata) because the confidence weighter
// reads it.
type EvidenceChunkKind string

// EvidenceKindLead tags a chunk from a document's lead section; EvidenceKindBody
// tags one from a later body section.
const (
	EvidenceKindLead EvidenceChunkKind = "lead"
	EvidenceKindBody EvidenceChunkKind = "body"
)

// Valid reports whether k is a known chunk kind. The evidence_chunks.kind column
// is plain text (no CHECK, so the runtime staging table stays byte-identical
// under LIKE INCLUDING DEFAULTS), so the store guards writes with this instead.
func (k EvidenceChunkKind) Valid() bool {
	switch k {
	case EvidenceKindLead, EvidenceKindBody:
		return true
	default:
		return false
	}
}

// EvidenceChunk is one embedded-or-pending chunk of an evidence source. Identity
// is the generic natural key (Source, ExternalID, ChunkIndex): Source is the
// discriminator (a Wikipedia corpus name, a statistical source), ExternalID is
// whatever stable id that source assigns a document (a Wikipedia page id, a
// statistical series key), and ChunkIndex is the chunk's ordinal within it.
// Kind is the typed lead/body classification the confidence weighter reads.
// Embedding is nil at ingest and filled by the bulk-embedding pipeline, which
// reads the stored Content, embeds it, and loads the completed chunk. Metadata
// is source-specific provenance (a Wikipedia revision id and section, the
// clustering outputs, or whatever a new source needs) stored verbatim as jsonb,
// so adding a source is rows under a new Source value with its own metadata
// keys - never a migration and never a new column. Use WikiMetadata to build
// and read the wiki/crawl keys.
type EvidenceChunk struct {
	Source     string
	ExternalID string
	ChunkIndex int
	Title      string
	URL        string
	Content    string
	Kind       EvidenceChunkKind
	Embedding  []float32
	Metadata   map[string]any
	// PublishedAt is the passage's real-world publication (or document) date,
	// nil when the source is genuinely undated (encyclopedic content). It is a
	// typed column, not a metadata key, because retrieval filters and orders on
	// it; ingestion sets it only from a date the source exposes, never a guess.
	PublishedAt *time.Time
}

// EvidenceHit is a retrieval hit from the evidence corpus: a chunk's source
// attribution and excerpt with its cosine distance to the query in [0, 2],
// lower is more similar. Evidence is supporting context, never a verdict, so
// this type carries no verdict. Source, ExternalID, and ChunkIndex are the
// chunk's stable coordinates - the same natural key the store writes under - so
// a verifier's citation can round-trip back to the exact source row via
// ComposeEvidenceID. Section is surfaced from the chunk's metadata for citation
// display.
type EvidenceHit struct {
	Source     string
	ExternalID string
	ChunkIndex int
	Title      string
	URL        string
	Content    string
	Kind       EvidenceChunkKind
	Section    string
	Distance   float32
	// PublishedAt mirrors the chunk's publication date so downstream judging
	// can label a passage with when it was true; nil for undated sources.
	PublishedAt *time.Time
}

// EvidenceTrim marks the chunks of a document that a sync run did not
// (re)produce: every chunk of (Source, ExternalID) with index >= FromIndex is
// stale and must go. FromIndex 0 removes the document entirely.
type EvidenceTrim struct {
	Source     string
	ExternalID string
	FromIndex  int
}

// EvidenceCursor is a keyset position in (source, external_id, chunk_index)
// order over evidence_chunks. It includes Source because external_id is unique
// only within a source, so an unfiltered keyset scan across every source needs
// the full key to page without skipping or repeating a row. The zero value
// sorts before every real chunk, so it means "from the beginning".
type EvidenceCursor struct {
	Source     string
	ExternalID string
	ChunkIndex int32
}

// EvidenceRemaining counts the chunks still to embed beyond a cursor: distinct
// documents, chunks, and total content characters (the token-estimate input).
type EvidenceRemaining struct {
	Documents int64
	Chunks    int64
	Chars     int64
}

// EvidenceCorpusHealth is the live corpus snapshot the reingest verifier checks:
// the total chunk count, the counts of rows that would make the corpus
// unservable (a missing or zero-vector embedding, an absent kind), the embedding
// column's declared type (proving the dimension), and whether the HNSW index
// exists and is valid. A healthy corpus has Chunks > 0, the three bad-row counts
// at zero, EmbeddingType matching the pinned dimension, and HNSWPresent and
// HNSWValid both true.
type EvidenceCorpusHealth struct {
	Chunks          int64
	NullEmbeddings  int64
	ZeroVectors     int64
	MissingMetadata int64
	EmbeddingType   string
	HNSWPresent     bool
	HNSWValid       bool
}

// EvidenceSyncState is the per-source ingestion checkpoint. LastChangeTS is the
// dump's publication time after a bulk run - an upper bound on the data snapshot
// cut, which happens days earlier, so the delta sync must resume with an overlap
// window rather than from this instant. DumpVersion records which dump produced
// the corpus. Both are zero until the first bulk run completes.
type EvidenceSyncState struct {
	Source       string
	LastChangeTS time.Time
	DumpVersion  string
}

// Metadata keys for the wiki and crawl sources. They are the jsonb payload the
// wiki-shaped columns (revision_id, section, cluster_id, importance) became when
// the corpus generalized; a non-wiki source simply uses different keys.
const (
	metaRevisionID = "revision_id"
	metaSection    = "section"
	metaClusterID  = "cluster_id"
	metaImportance = "importance"
)

// Metadata keys the near-duplicate gate (VER-203) writes on a chunk it withholds
// from search. MetaDuplicate flags the chunk; MetaDuplicateSimilarity records the
// cosine similarity to the nearest same-source neighbor that tripped the gate,
// so the flag and the evidence that produced it travel together. A flagged chunk
// is stored for provenance but carries no embedding, so every search (all filter
// embedding IS NOT NULL) skips it and the HNSW index never holds it.
const (
	MetaDuplicate           = "duplicate"
	MetaDuplicateSimilarity = "duplicate_similarity"
)

// WithDuplicateFlag returns a copy of metadata marked as a near-duplicate at the
// measured cosine similarity. It copies rather than mutates so the caller's map
// is left untouched, and it always returns a non-nil map so the store's jsonb
// marshaling never sees SQL NULL.
func WithDuplicateFlag(metadata map[string]any, similarity float64) map[string]any {
	out := make(map[string]any, len(metadata)+2)
	for k, v := range metadata {
		out[k] = v
	}
	out[MetaDuplicate] = true
	out[MetaDuplicateSimilarity] = similarity
	return out
}

// WikiMetadata is the typed view of a wiki/crawl chunk's EvidenceChunk.Metadata:
// the source revision the text came from, the section heading it sits under (""
// for a lead), and the offline clustering outputs (a topic cluster id and a
// [0,1] importance the producer maps to embedding-job priority). ClusterID and
// Importance are nil until the clustering job has run. It keeps the wiki
// pipeline typed while the column stays generic jsonb.
type WikiMetadata struct {
	RevisionID int64
	Section    string
	ClusterID  *int32
	Importance *float64
}

// Map renders the metadata as the jsonb payload. revision_id and section are
// always written (section's empty value is meaningful); cluster_id and
// importance appear only once the clustering job has set them, so a chunk that
// was never clustered carries no null clustering keys.
func (m WikiMetadata) Map() map[string]any {
	out := map[string]any{
		metaRevisionID: m.RevisionID,
		metaSection:    m.Section,
	}
	if m.ClusterID != nil {
		out[metaClusterID] = int64(*m.ClusterID)
	}
	if m.Importance != nil {
		out[metaImportance] = *m.Importance
	}
	return out
}

// ParseWikiMetadata reads the wiki/crawl keys out of a chunk's metadata map, the
// inverse of Map. It tolerates float64 for the numeric keys because pgx decodes
// every jsonb number as float64. An absent key is the zero value, not an error,
// so a chunk from a source that carries no wiki provenance parses to the zero
// WikiMetadata cleanly.
func ParseWikiMetadata(m map[string]any) (WikiMetadata, error) {
	var wm WikiMetadata
	if v, ok := m[metaRevisionID]; ok {
		n, err := metaInt64(v)
		if err != nil {
			return WikiMetadata{}, fmt.Errorf("domain: metadata %s: %w", metaRevisionID, err)
		}
		wm.RevisionID = n
	}
	if v, ok := m[metaSection]; ok {
		s, ok := v.(string)
		if !ok {
			return WikiMetadata{}, fmt.Errorf("domain: metadata %s: want string, got %T", metaSection, v)
		}
		wm.Section = s
	}
	if v, ok := m[metaClusterID]; ok {
		n, err := metaInt64(v)
		if err != nil {
			return WikiMetadata{}, fmt.Errorf("domain: metadata %s: %w", metaClusterID, err)
		}
		c := int32(n)
		wm.ClusterID = &c
	}
	if v, ok := m[metaImportance]; ok {
		f, err := metaFloat64(v)
		if err != nil {
			return WikiMetadata{}, fmt.Errorf("domain: metadata %s: %w", metaImportance, err)
		}
		wm.Importance = &f
	}
	return wm, nil
}

// metaInt64 coerces a metadata number to int64, accepting the int64 that Map
// writes and the float64 that pgx yields when decoding jsonb.
func metaInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case float64:
		return int64(n), nil
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("want number, got %T", v)
	}
}

// metaFloat64 coerces a metadata number to float64, accepting both the float64
// that Map writes and any integer form.
func metaFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("want number, got %T", v)
	}
}
