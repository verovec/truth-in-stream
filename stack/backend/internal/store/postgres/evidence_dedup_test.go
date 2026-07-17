package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// getChunkForTest reads a chunk's metadata and whether its embedding is NULL
// straight from the pool, so a test can assert what the dedup gate persisted
// without a public accessor.
func getChunkForTest(ctx context.Context, s *Store, source, externalID string, chunkIndex int) (map[string]any, bool, bool, error) {
	var raw []byte
	var embeddingIsNull bool
	err := s.pool.QueryRow(
		ctx,
		"SELECT metadata, embedding IS NULL FROM evidence_chunks WHERE source = $1 AND external_id = $2 AND chunk_index = $3",
		source, externalID, chunkIndex,
	).Scan(&raw, &embeddingIsNull)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, false, nil
		}
		return nil, false, false, err
	}
	meta, err := unmarshalMetadata(raw)
	if err != nil {
		return nil, false, false, err
	}
	return meta, embeddingIsNull, true, nil
}

// TestContentAlreadyEmbedded exercises the content-hash short-circuit against a
// live corpus: it is false for an absent chunk, false for one stored but not yet
// embedded, false when the content changed, and true only when the exact content
// is already embedded at that key.
func TestContentAlreadyEmbedded(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunk := embeddedEvidence("insee-emploi", "series-1", unitVec(0))

	// Absent.
	if ok, err := store.ContentAlreadyEmbedded(ctx, chunk); err != nil || ok {
		t.Fatalf("absent chunk: ok=%v err=%v, want false/nil", ok, err)
	}

	// Present but unembedded (UpsertChunks never writes a vector).
	unembedded := chunk
	unembedded.Embedding = nil
	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{unembedded}); err != nil {
		t.Fatalf("upsert unembedded: %v", err)
	}
	if ok, err := store.ContentAlreadyEmbedded(ctx, chunk); err != nil || ok {
		t.Fatalf("unembedded chunk: ok=%v err=%v, want false/nil", ok, err)
	}

	// Embedded with the same content: short-circuit fires.
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		t.Fatalf("upsert embedded: %v", err)
	}
	if ok, err := store.ContentAlreadyEmbedded(ctx, chunk); err != nil || !ok {
		t.Fatalf("embedded chunk: ok=%v err=%v, want true/nil", ok, err)
	}

	// Same key, different content: hash differs, so no short-circuit.
	changed := chunk
	changed.Content = chunk.Content + " revised"
	if ok, err := store.ContentAlreadyEmbedded(ctx, changed); err != nil || ok {
		t.Fatalf("changed content: ok=%v err=%v, want false/nil", ok, err)
	}
}

// TestContentAlreadyEmbeddedBackslashEscapeContent pins the fingerprint to the
// content's literal bytes. Both backslash sequences here are VALID bytea escape
// syntax ('\\' and octal '\101'), so a cast-based column hash would silently
// collapse them to different bytes than the Go-side sha256 of the text and the
// short-circuit would never fire for such content - an eternal re-embed instead
// of a crash. The probe must report true once this exact content is embedded.
func TestContentAlreadyEmbeddedBackslashEscapeContent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunk := embeddedEvidence("insee-emploi", "series-1", unitVec(0))
	chunk.Content = `un backslash double \\ et un octal \101 dans le texte`
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		t.Fatalf("upsert embedded: %v", err)
	}
	if ok, err := store.ContentAlreadyEmbedded(ctx, chunk); err != nil || !ok {
		t.Fatalf("escape-sequence content: ok=%v err=%v, want true/nil", ok, err)
	}
}

// TestUpsertEmbeddedChunkDedupedGateOff proves that with the bar at zero the
// deduped upsert is exactly a plain embedded upsert: the chunk is stored with its
// vector and is searchable, never flagged.
func TestUpsertEmbeddedChunkDedupedGateOff(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunk := embeddedEvidence("insee-emploi", "series-1", unitVec(0))
	flagged, err := store.UpsertEmbeddedChunkDeduped(ctx, chunk, 0)
	if err != nil {
		t.Fatalf("deduped upsert: %v", err)
	}
	if flagged {
		t.Fatal("gate off: chunk was flagged, want not flagged")
	}
	hits, err := store.SearchEvidence(ctx, unitVec(0), 5, 0, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("search returned %d hits, want 1", len(hits))
	}
}

// TestUpsertEmbeddedChunkDedupedFlagsNearDuplicate proves the gate withholds a
// near-identical chunk in the same source: it is stored for provenance with the
// duplicate flag and no embedding, so it is excluded from search and from the
// live un-embedded scan (never re-embedded), while a distinct chunk is served.
func TestUpsertEmbeddedChunkDedupedFlagsNearDuplicate(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	original := embeddedEvidence("insee-emploi", "series-1", unitVec(0))
	if _, err := store.UpsertEmbeddedChunkDeduped(ctx, original, 0.9); err != nil {
		t.Fatalf("upsert original: %v", err)
	}

	// A second chunk with an identical vector (cosine similarity 1) in the same
	// source trips the 0.9 bar.
	dup := embeddedEvidence("insee-emploi", "series-2", unitVec(0))
	dup.Content = "the same unemployment figure, restated"
	flagged, err := store.UpsertEmbeddedChunkDeduped(ctx, dup, 0.9)
	if err != nil {
		t.Fatalf("upsert duplicate: %v", err)
	}
	if !flagged {
		t.Fatal("near-duplicate was not flagged")
	}

	// The flagged row is kept for provenance but withheld from search.
	meta, embeddingIsNull, ok, err := getChunkForTest(ctx, store, "insee-emploi", "series-2", 0)
	if err != nil {
		t.Fatalf("get flagged chunk: %v", err)
	}
	if !ok {
		t.Fatal("flagged chunk was deleted, want kept for provenance")
	}
	if !embeddingIsNull {
		t.Error("flagged chunk kept its embedding, want NULL so it is unindexed and unsearchable")
	}
	if meta[domain.MetaDuplicate] != true {
		t.Errorf("flagged chunk missing duplicate metadata: %+v", meta)
	}

	// Only the original is searchable; the duplicate has no vector.
	hits, err := store.SearchEvidence(ctx, unitVec(0), 5, 0, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].ExternalID != "series-1" {
		t.Fatalf("search hits = %+v, want only series-1", hits)
	}

	// The gate must not leave the duplicate as pending embed work.
	n, err := store.CountUnembeddedLive(ctx)
	if err != nil {
		t.Fatalf("count unembedded: %v", err)
	}
	if n != 0 {
		t.Errorf("CountUnembeddedLive = %d, want 0 (duplicate must not be re-embedded)", n)
	}
}

// TestUnembeddedChunksExcludesDuplicates proves the delta-sync publish scan
// (UnembeddedChunks, backing internal/wiki/delta.go) skips a near-duplicate row
// the gate withheld. The delta sync scans the whole shared table, so without the
// exclusion it would re-embed and re-serve a duplicate-flagged row and defeat the
// gate; a genuinely-unembedded chunk is still returned.
func TestUnembeddedChunksExcludesDuplicates(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// An embedded original plus a near-identical chunk the gate flags (stored with
	// no vector + duplicate metadata) in the same source.
	if _, err := store.UpsertEmbeddedChunkDeduped(ctx, embeddedEvidence("insee-emploi", "series-1", unitVec(0)), 0.9); err != nil {
		t.Fatalf("upsert original: %v", err)
	}
	dup := embeddedEvidence("insee-emploi", "series-2", unitVec(0))
	dup.Content = "the same figure, restated"
	if flagged, err := store.UpsertEmbeddedChunkDeduped(ctx, dup, 0.9); err != nil || !flagged {
		t.Fatalf("expected flagged duplicate: flagged=%v err=%v", flagged, err)
	}

	// A genuinely unembedded chunk that the delta sync SHOULD pick up.
	pending := embeddedEvidence("insee-emploi", "series-3", unitVec(1))
	pending.Embedding = nil
	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{pending}); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}

	got, err := store.UnembeddedChunks(ctx, domain.EvidenceCursor{}, 100)
	if err != nil {
		t.Fatalf("unembedded chunks: %v", err)
	}
	for _, c := range got {
		if c.ExternalID == "series-2" {
			t.Fatalf("delta scan returned the duplicate-flagged row; it would be re-embedded: %+v", c)
		}
	}
	// The real pending chunk is still returned, so the exclusion is scoped to
	// duplicates, not all NULL-embedding rows.
	var sawPending bool
	for _, c := range got {
		if c.ExternalID == "series-3" {
			sawPending = true
		}
	}
	if !sawPending {
		t.Fatalf("delta scan dropped a genuinely-unembedded chunk; got %d rows", len(got))
	}
}

// TestUpsertEmbeddedChunkDedupedKeepsDistinct proves a chunk below the bar is
// embedded and served normally even with the gate on.
func TestUpsertEmbeddedChunkDedupedKeepsDistinct(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if _, err := store.UpsertEmbeddedChunkDeduped(ctx, embeddedEvidence("insee-emploi", "series-1", unitVec(0)), 0.9); err != nil {
		t.Fatalf("upsert original: %v", err)
	}
	// An orthogonal vector (cosine similarity 0) is far below the bar.
	distinct := embeddedEvidence("insee-emploi", "series-2", unitVec(5))
	flagged, err := store.UpsertEmbeddedChunkDeduped(ctx, distinct, 0.9)
	if err != nil {
		t.Fatalf("upsert distinct: %v", err)
	}
	if flagged {
		t.Fatal("distinct chunk was wrongly flagged")
	}
	hits, err := store.SearchEvidence(ctx, unitVec(5), 5, 0, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// Both distinct chunks are embedded and served (neither was wrongly deduped); a
	// top-k search over the two returns both, nearest first, so series-2 (the query
	// vector) leads and series-1 (orthogonal) follows.
	if len(hits) != 2 || hits[0].ExternalID != "series-2" {
		t.Fatalf("search hits = %+v, want both distinct chunks served with series-2 nearest", hits)
	}
}
