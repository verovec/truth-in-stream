package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

func wikiChunk(pageID int64, idx int, content string) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:     "simplewiki",
		ExternalID: strconv.FormatInt(pageID, 10),
		ChunkIndex: idx,
		Title:      "Paris",
		URL:        "https://simple.wikipedia.org/wiki/Paris",
		Content:    content,
		Kind:       domain.EvidenceKindLead,
		Metadata:   domain.WikiMetadata{RevisionID: 100}.Map(),
	}
}

// chunkMeta decodes a stored jsonb metadata payload back into the typed wiki
// view, so a test can assert the revision/section a chunk carries without
// comparing raw jsonb bytes (whose key order and spacing Postgres normalizes).
func chunkMeta(t *testing.T, raw []byte) domain.WikiMetadata {
	t.Helper()
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}
	}
	wm, err := domain.ParseWikiMetadata(m)
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	return wm
}

// setEmbedding writes an embedding onto an existing chunk so it becomes
// searchable, mirroring what the bulk-embedding swap leaves behind.
func setEmbedding(ctx context.Context, t *testing.T, store *Store, pageID int64, idx int, v []float32) {
	t.Helper()
	emb := pgvector.NewHalfVector(v)
	if _, err := store.pool.Exec(
		ctx,
		"UPDATE evidence_chunks SET embedding = $1 WHERE source = 'simplewiki' AND external_id = $2 AND chunk_index = $3",
		emb, strconv.FormatInt(pageID, 10), idx,
	); err != nil {
		t.Fatalf("set embedding page %d chunk %d: %v", pageID, idx, err)
	}
}

func TestSearchWikiOrdersByCosineDistanceAndExcludesUnembedded(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.EvidenceChunk{
		{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0, Title: "Alpha", URL: "https://w/alpha", Content: "alpha lead", Kind: domain.EvidenceKindLead, Metadata: domain.WikiMetadata{RevisionID: 1}.Map()},
		{Source: "simplewiki", ExternalID: "2", ChunkIndex: 0, Title: "Bravo", URL: "https://w/bravo", Content: "bravo lead", Kind: domain.EvidenceKindLead, Metadata: domain.WikiMetadata{RevisionID: 1}.Map()},
		{Source: "simplewiki", ExternalID: "3", ChunkIndex: 0, Title: "Charlie", URL: "https://w/charlie", Content: "charlie lead, never embedded", Kind: domain.EvidenceKindLead, Metadata: domain.WikiMetadata{RevisionID: 1}.Map()},
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	setEmbedding(ctx, t, store, 1, 0, unitVec(0))
	setEmbedding(ctx, t, store, 2, 0, unitVec(1))
	// Page 3 stays unembedded and must never surface.

	tests := []struct {
		name      string
		query     []float32
		topK      int
		wantFirst string
		wantLen   int
	}{
		{name: "nearest is alpha", query: unitVec(0), topK: 5, wantFirst: "Alpha", wantLen: 2},
		{name: "nearest is bravo", query: unitVec(1), topK: 5, wantFirst: "Bravo", wantLen: 2},
		{name: "topK truncates", query: unitVec(0), topK: 1, wantFirst: "Alpha", wantLen: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.SearchEvidence(ctx, tc.query, tc.topK, 0, nil)
			if err != nil {
				t.Fatalf("SearchEvidence: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("got %d evidence, want %d", len(got), tc.wantLen)
			}
			if got[0].Title != tc.wantFirst {
				t.Fatalf("nearest = %q, want %q", got[0].Title, tc.wantFirst)
			}
			if got[0].Distance > 1e-4 {
				t.Errorf("nearest distance = %v, want ~0", got[0].Distance)
			}
			for _, e := range got {
				if e.Title == "Charlie" {
					t.Errorf("unembedded chunk surfaced in evidence")
				}
				if e.URL == "" || e.Content == "" {
					t.Errorf("evidence missing attribution: %+v", e)
				}
			}
		})
	}
}

func TestSearchWikiRejectsWrongDimension(t *testing.T) {
	store := setupStore(t)
	if _, err := store.SearchEvidence(t.Context(), []float32{1, 2, 3}, 5, 0, nil); err == nil {
		t.Fatal("SearchEvidence with wrong dimension: want error, got nil")
	}
}

func TestUpsertChunksRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.EvidenceChunk{
		wikiChunk(1, 0, "Paris\n\nParis is the capital of France."),
		wikiChunk(1, 1, "Paris\n\nIt sits on the Seine."),
		wikiChunk(2, 0, "Lyon\n\nLyon is a city in France."),
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	got, err := store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{Source: "simplewiki", ExternalID: "1", ChunkIndex: 1})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	want := db.GetEvidenceChunkRow{
		Source:          "simplewiki",
		ExternalID:      "1",
		ChunkIndex:      1,
		Title:           "Paris",
		Url:             "https://simple.wikipedia.org/wiki/Paris",
		Content:         "Paris\n\nIt sits on the Seine.",
		Kind:            "lead",
		EmbeddingIsNull: true,
	}
	gotMeta := chunkMeta(t, got.Metadata)
	got.Metadata = nil
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("stored chunk mismatch (-want +got):\n%s", diff)
	}
	if gotMeta.RevisionID != 100 || gotMeta.Section != "" {
		t.Errorf("stored metadata = %+v, want revision 100 / empty section", gotMeta)
	}
}

func TestUpsertChunksPersistsSectionAndKind(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunk := wikiChunk(1, 0, "Paris\n\nParis is the capital of France.")
	chunk.Metadata = domain.WikiMetadata{RevisionID: 100, Section: "History"}.Map()
	chunk.Kind = domain.EvidenceKindBody
	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{chunk}); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	got, err := store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	if section := chunkMeta(t, got.Metadata).Section; section != "History" || got.Kind != "body" {
		t.Errorf("stored (section, kind) = (%q, %q), want (History, body)", section, got.Kind)
	}
}

func TestWikiChunkMetadataColumnsBackfillToDefaults(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// A row written without the metadata jsonb or the kind column - any insert
	// that omits them - falls to the column defaults: an empty metadata object
	// (so an empty section) and the lead kind. This is the additive default
	// guarantee that lets ingest omit provenance it does not have without a data
	// backfill step.
	if _, err := store.pool.Exec(
		ctx,
		"INSERT INTO evidence_chunks (source, external_id, chunk_index, title, url, content) VALUES ('simplewiki', '1', 0, 'Paris', 'https://w/p', 'lead text')",
	); err != nil {
		t.Fatalf("insert without metadata: %v", err)
	}
	got, err := store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	if section := chunkMeta(t, got.Metadata).Section; section != "" || got.Kind != "lead" {
		t.Errorf("defaulted (section, kind) = (%q, %q), want (\"\", lead)", section, got.Kind)
	}
}

func TestSearchWikiCarriesSectionAndKind(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunk := wikiChunk(8675309, 3, "alpha body")
	chunk.Metadata = domain.WikiMetadata{RevisionID: 100, Section: "History"}.Map()
	chunk.Kind = domain.EvidenceKindBody
	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{chunk}); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	setEmbedding(ctx, t, store, 8675309, 3, unitVec(0))

	got, err := store.SearchEvidence(ctx, unitVec(0), 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d evidence, want 1", len(got))
	}
	if got[0].Section != "History" || got[0].Kind != domain.EvidenceKindBody {
		t.Errorf("evidence (section, kind) = (%q, %q), want (History, body)", got[0].Section, got[0].Kind)
	}
	// The (source, external_id, chunk_index) coordinates must survive retrieval so
	// a composed evidence_id resolves back to this exact row.
	if got[0].Source != "simplewiki" || got[0].ExternalID != "8675309" || got[0].ChunkIndex != 3 {
		t.Errorf("evidence coordinates = (%q, %q, %d), want (simplewiki, 8675309, 3)", got[0].Source, got[0].ExternalID, got[0].ChunkIndex)
	}
}

func TestUpsertChunksRejectsInvalidKind(t *testing.T) {
	store := setupStore(t)
	chunk := wikiChunk(1, 0, "Paris\n\nLead.")
	chunk.Kind = "bogus"
	if err := store.UpsertChunks(t.Context(), []domain.EvidenceChunk{chunk}); err == nil {
		t.Fatal("UpsertChunks accepted an invalid kind, want error")
	}
}

func TestUpsertStagingChunksRejectsInvalidKind(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if err := store.ResetStaging(ctx, "v1"); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	chunk := wikiChunk(1, 0, "Paris\n\nLead.")
	chunk.Kind = "bogus"
	if err := store.UpsertStagingChunks(ctx, []domain.EvidenceChunk{chunk}); err == nil {
		t.Fatal("UpsertStagingChunks accepted an invalid kind, want error")
	}
}

func TestUpsertChunksIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.EvidenceChunk{
		wikiChunk(1, 0, "Paris\n\nFirst."),
		wikiChunk(1, 1, "Paris\n\nSecond."),
	}
	for range 2 {
		if err := store.UpsertChunks(ctx, chunks); err != nil {
			t.Fatalf("UpsertChunks: %v", err)
		}
	}

	n, err := store.queries.CountEvidenceChunksForDocument(ctx, db.CountEvidenceChunksForDocumentParams{Source: "simplewiki", ExternalID: "1"})
	if err != nil {
		t.Fatalf("CountEvidenceChunksForDocument: %v", err)
	}
	if n != 2 {
		t.Errorf("page 1 has %d chunks after re-run, want 2", n)
	}
}

func TestUpsertChunksEmbeddingInvalidation(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{wikiChunk(1, 0, "Paris\n\nOriginal.")}); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	emb := pgvector.NewHalfVector(unitVec(0))
	if _, err := store.pool.Exec(ctx, "UPDATE evidence_chunks SET embedding = $1 WHERE source = 'simplewiki' AND external_id = '1' AND chunk_index = 0", emb); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}

	// Same content: the embedding must survive the upsert.
	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{wikiChunk(1, 0, "Paris\n\nOriginal.")}); err != nil {
		t.Fatalf("UpsertChunks (same content): %v", err)
	}
	row, err := store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	if row.EmbeddingIsNull {
		t.Error("unchanged content dropped the embedding")
	}

	// Changed content: the stale embedding must be invalidated.
	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{wikiChunk(1, 0, "Paris\n\nRewritten.")}); err != nil {
		t.Fatalf("UpsertChunks (changed content): %v", err)
	}
	row, err = store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	if !row.EmbeddingIsNull {
		t.Error("changed content kept a stale embedding")
	}
}

// TestUpsertChunksBackslashContent proves the content fingerprint survives raw
// wiki text. A text-to-bytea CAST parses the content as a bytea escape literal,
// so a lone backslash (LaTeX markup on a real frwiki page) would abort the whole
// insert with 22P02; the generated column must hash the content's UTF-8 bytes
// instead, and the stored hash must equal the Go-side sha256 the dedup probe
// sends.
func TestUpsertChunksBackslashContent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	content := `Paris

En physique, l'énergie s'écrit E = mc^2 \quad \text{(relativité)}.`
	if err := store.UpsertChunks(ctx, []domain.EvidenceChunk{wikiChunk(1, 0, content)}); err != nil {
		t.Fatalf("UpsertChunks with backslash content: %v", err)
	}

	sum := sha256.Sum256([]byte(content))
	var stored []byte
	if err := store.pool.QueryRow(
		ctx,
		"SELECT content_hash FROM evidence_chunks WHERE source = 'simplewiki' AND external_id = '1' AND chunk_index = 0",
	).Scan(&stored); err != nil {
		t.Fatalf("read content_hash: %v", err)
	}
	if !bytes.Equal(stored, sum[:]) {
		t.Errorf("content_hash = %x, want the sha256 of the UTF-8 content %x", stored, sum)
	}
}

func TestTrimPages(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.EvidenceChunk{
		wikiChunk(1, 0, "Paris\n\nOne."),
		wikiChunk(1, 1, "Paris\n\nTwo."),
		wikiChunk(1, 2, "Paris\n\nThree."),
		wikiChunk(2, 0, "Lyon\n\nOther page."),
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	trims := []domain.EvidenceTrim{
		{Source: "simplewiki", ExternalID: "1", FromIndex: 1},
		{Source: "simplewiki", ExternalID: "2", FromIndex: 0},
	}
	if err := store.TrimDocuments(ctx, trims); err != nil {
		t.Fatalf("TrimDocuments: %v", err)
	}

	n, err := store.queries.CountEvidenceChunksForDocument(ctx, db.CountEvidenceChunksForDocumentParams{Source: "simplewiki", ExternalID: "1"})
	if err != nil {
		t.Fatalf("CountEvidenceChunksForDocument(1): %v", err)
	}
	if n != 1 {
		t.Errorf("page 1 has %d chunks after trim from 1, want 1", n)
	}
	n, err = store.queries.CountEvidenceChunksForDocument(ctx, db.CountEvidenceChunksForDocumentParams{Source: "simplewiki", ExternalID: "2"})
	if err != nil {
		t.Fatalf("CountEvidenceChunksForDocument(2): %v", err)
	}
	if n != 0 {
		t.Errorf("page 2 has %d chunks after trim from 0, want 0", n)
	}
}

func TestEnsureCorpus(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource (fresh store): %v", err)
	}
	// Idempotent for the same corpus, even before any checkpoint exists.
	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource (same corpus): %v", err)
	}
	// A different corpus is refused: page ids would collide. The refusal must
	// carry the sentinel so optional claimants (the dev seed) can skip.
	if err := store.EnsureSource(ctx, "enwiki"); !errors.Is(err, domain.ErrEvidenceSourceConflict) {
		t.Fatalf("EnsureSource second corpus: err = %v, want ErrEvidenceSourceConflict", err)
	}

	// The claim must not have created a fake checkpoint.
	st, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !ok {
		t.Fatal("corpus claim row missing")
	}
	if !st.LastChangeTS.IsZero() || st.DumpVersion != "" {
		t.Errorf("corpus claim invented a checkpoint: %+v", st)
	}
}

func TestSyncStateRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	_, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil {
		t.Fatalf("GetSyncState (empty): %v", err)
	}
	if ok {
		t.Fatal("GetSyncState reported a checkpoint before any sync")
	}

	ts := time.Date(2026, 6, 1, 3, 14, 0, 0, time.UTC)
	st := domain.EvidenceSyncState{Source: "simplewiki", LastChangeTS: ts, DumpVersion: "Mon, 01 Jun 2026 03:14:00 GMT"}
	if err := store.SetSyncState(ctx, st); err != nil {
		t.Fatalf("SetSyncState: %v", err)
	}

	got, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !ok {
		t.Fatal("GetSyncState found nothing after SetSyncState")
	}
	if diff := cmp.Diff(st, got); diff != "" {
		t.Errorf("sync state mismatch (-want +got):\n%s", diff)
	}

	// Re-checkpoint replaces the row, never duplicates it.
	st.DumpVersion = "Mon, 08 Jun 2026 03:14:00 GMT"
	st.LastChangeTS = ts.AddDate(0, 0, 7)
	if err := store.SetSyncState(ctx, st); err != nil {
		t.Fatalf("SetSyncState (update): %v", err)
	}
	got, ok, err = store.GetSyncState(ctx, "simplewiki")
	if err != nil || !ok {
		t.Fatalf("GetSyncState (after update): ok=%v err=%v", ok, err)
	}
	if diff := cmp.Diff(st, got); diff != "" {
		t.Errorf("updated sync state mismatch (-want +got):\n%s", diff)
	}
}

func TestSyncStateZeroTimeStoresNull(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	st := domain.EvidenceSyncState{Source: "simplewiki", DumpVersion: "unknown"}
	if err := store.SetSyncState(ctx, st); err != nil {
		t.Fatalf("SetSyncState: %v", err)
	}

	got, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil || !ok {
		t.Fatalf("GetSyncState: ok=%v err=%v", ok, err)
	}
	if !got.LastChangeTS.IsZero() {
		t.Errorf("LastChangeTS = %v, want zero for a NULL checkpoint", got.LastChangeTS)
	}
}

func TestEnsureCorpusConcurrentClaims(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// Two concurrent syncs claiming different corpora on a fresh store: the
	// advisory lock serializes them, so exactly one wins.
	errs := make(chan error, 2)
	for _, corpus := range []string{"simplewiki", "enwiki"} {
		go func() { errs <- store.EnsureSource(ctx, corpus) }()
	}

	failures := 0
	for range 2 {
		if err := <-errs; err != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Errorf("%d of 2 concurrent foreign-corpus claims failed, want exactly 1", failures)
	}
}

// fullEmbedding returns a constant full-dimension vector for upsert round-trip
// tests; its exact values are irrelevant, only that it is the right shape.
func fullEmbedding() []float32 {
	v := make([]float32, domain.EmbeddingDim)
	for i := range v {
		v[i] = 0.01
	}
	return v
}

func TestUpsertEmbeddedChunkRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunk := domain.EvidenceChunk{
		Source: "simplewiki-crawl", ExternalID: "7", ChunkIndex: 0, Title: "Atom", URL: "https://simple.wikipedia.org/wiki/Atom",
		Content: "Atom\n\nAn atom is matter.", Kind: domain.EvidenceKindBody, Embedding: fullEmbedding(),
		Metadata: domain.WikiMetadata{RevisionID: 11}.Map(),
	}
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		t.Fatalf("UpsertEmbeddedChunk: %v", err)
	}

	got, err := store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{Source: "simplewiki-crawl", ExternalID: "7", ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	if got.Content != chunk.Content {
		t.Errorf("content = %q, want %q", got.Content, chunk.Content)
	}
	if got.Kind != "body" {
		t.Errorf("kind = %q, want body", got.Kind)
	}
	if got.Source != "simplewiki-crawl" {
		t.Errorf("source = %q, want simplewiki-crawl", got.Source)
	}
	if rev := chunkMeta(t, got.Metadata).RevisionID; rev != 11 {
		t.Errorf("revision = %d, want 11", rev)
	}
	if got.EmbeddingIsNull {
		t.Error("embedding is null, want a stored vector")
	}
}

func TestUpsertEmbeddedChunkIsIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunk := domain.EvidenceChunk{
		Source: "simplewiki-crawl", ExternalID: "9", ChunkIndex: 0, Title: "Ion", URL: "u",
		Content: "Ion\n\ntext", Kind: domain.EvidenceKindLead, Embedding: fullEmbedding(),
		Metadata: domain.WikiMetadata{RevisionID: 1}.Map(),
	}
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Re-apply with new content; the row is replaced in place, not duplicated.
	chunk.Content = "Ion\n\nrevised"
	chunk.Metadata = domain.WikiMetadata{RevisionID: 2}.Map()
	if err := store.UpsertEmbeddedChunk(ctx, chunk); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	got, err := store.queries.GetEvidenceChunk(ctx, db.GetEvidenceChunkParams{Source: "simplewiki-crawl", ExternalID: "9", ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetEvidenceChunk: %v", err)
	}
	if got.Content != "Ion\n\nrevised" || chunkMeta(t, got.Metadata).RevisionID != 2 || got.EmbeddingIsNull {
		t.Errorf("re-apply mismatch: content=%q rev=%d nullVec=%v", got.Content, chunkMeta(t, got.Metadata).RevisionID, got.EmbeddingIsNull)
	}
}

func TestUpsertEmbeddedChunkRejectsWrongDim(t *testing.T) {
	store := setupStore(t)
	chunk := domain.EvidenceChunk{
		Source: "c", ExternalID: "1", ChunkIndex: 0, Content: "x",
		Kind: domain.EvidenceKindLead, Embedding: make([]float32, 3),
	}
	if err := store.UpsertEmbeddedChunk(t.Context(), chunk); err == nil {
		t.Fatal("UpsertEmbeddedChunk with 3 dims = nil error, want error")
	}
}

func TestUpsertEmbeddedChunkRejectsInvalidKind(t *testing.T) {
	store := setupStore(t)
	chunk := domain.EvidenceChunk{
		Source: "c", ExternalID: "1", ChunkIndex: 0, Content: "x",
		Kind: domain.EvidenceChunkKind("sidebar"), Embedding: fullEmbedding(),
	}
	if err := store.UpsertEmbeddedChunk(t.Context(), chunk); err == nil {
		t.Fatal("UpsertEmbeddedChunk with invalid kind = nil error, want error")
	}
}
