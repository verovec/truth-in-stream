package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// createTestDocument inserts a pending document with a fresh caller-minted id,
// the way the document service does.
func createTestDocument(ctx context.Context, t *testing.T, store *Store, title string) domain.Document {
	t.Helper()
	id := uuid.NewString()
	doc, err := store.CreateDocument(ctx, domain.Document{
		ID:          id,
		Title:       title,
		ObjectKey:   domain.DocumentObjectKey(id),
		ContentType: "application/pdf",
		SizeBytes:   2048,
		Status:      domain.DocumentStatusPending,
	})
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	return doc
}

func TestCreateAndGetDocument(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created := createTestDocument(ctx, t, store, "Rapport")
	if created.ID == "" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created record incomplete: %+v", created)
	}
	if created.Status != domain.DocumentStatusPending {
		t.Errorf("status = %q, want pending", created.Status)
	}
	if created.AnalysisStatus != domain.DocumentAnalysisNone {
		t.Errorf("analysis status = %q, want none", created.AnalysisStatus)
	}

	got, err := store.GetDocument(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.ID != created.ID || got.Title != "Rapport" || got.ObjectKey != domain.DocumentObjectKey(created.ID) {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.PageCount != 0 || got.SentencesTotal != 0 || got.SentencesProcessed != 0 || got.AnalysisRuns != 0 {
		t.Errorf("fresh document carries analysis state: %+v", got)
	}
	if !got.AnalyzedAt.IsZero() {
		t.Errorf("analyzed_at = %v, want zero", got.AnalyzedAt)
	}
}

func TestCreateDocumentRejectsInvalidInput(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if _, err := store.CreateDocument(ctx, domain.Document{
		ID: "not-a-uuid", Title: "x", ObjectKey: "documents/x/original.pdf",
		ContentType: "application/pdf", SizeBytes: 1, Status: domain.DocumentStatusPending,
	}); err == nil {
		t.Error("malformed id accepted")
	}
	if _, err := store.CreateDocument(ctx, domain.Document{
		ID: uuid.NewString(), Title: "x", ObjectKey: "documents/y/original.pdf",
		ContentType: "application/pdf", SizeBytes: 1, Status: "uploaded",
	}); err == nil {
		t.Error("invalid status accepted")
	}
}

func TestGetDocumentNotFound(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if _, err := store.GetDocument(ctx, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("absent id err = %v, want ErrDocumentNotFound", err)
	}
	if _, err := store.GetDocument(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("malformed id err = %v, want ErrDocumentNotFound", err)
	}
}

func TestStoreDocumentExtraction(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := createTestDocument(ctx, t, store, "Article")
	sentences := []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Première phrase.", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "Deuxième phrase.", Occurrence: 1},
		{Seq: 2, Page: 2, Text: "Deuxième phrase.", Occurrence: 1},
	}
	updated, err := store.StoreDocumentExtraction(ctx, doc.ID, 2, sentences)
	if err != nil {
		t.Fatalf("StoreDocumentExtraction: %v", err)
	}
	if updated.Status != domain.DocumentStatusReady {
		t.Errorf("status = %q, want ready", updated.Status)
	}
	if updated.PageCount != 2 || updated.SentencesTotal != 3 {
		t.Errorf("page count/sentences = %d/%d, want 2/3", updated.PageCount, updated.SentencesTotal)
	}
	if updated.AnalysisStatus != domain.DocumentAnalysisNone {
		t.Errorf("analysis status = %q, want none (the analyzer card wires auto-start)", updated.AnalysisStatus)
	}

	stored, err := store.ListDocumentSentences(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentSentences: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("stored %d sentences, want 3", len(stored))
	}
	for i, s := range stored {
		if s.Seq != i {
			t.Errorf("sentence %d out of order: seq %d", i, s.Seq)
		}
		if s.DocumentID != doc.ID {
			t.Errorf("sentence %d document id = %q", i, s.DocumentID)
		}
		if s.SkipReason != domain.SkipReasonNone {
			t.Errorf("sentence %d skip reason = %q, want none", i, s.SkipReason)
		}
	}
	if stored[2].Page != 2 || stored[2].Text != "Deuxième phrase." || stored[2].Occurrence != 1 {
		t.Errorf("sentence 2 round trip mismatch: %+v", stored[2])
	}
}

func TestStoreDocumentExtractionGuards(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	sentences := []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "Une phrase.", Occurrence: 1}}

	if _, err := store.StoreDocumentExtraction(ctx, "11111111-1111-1111-1111-111111111111", 1, sentences); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("unknown id err = %v, want ErrDocumentNotFound", err)
	}
	if _, err := store.StoreDocumentExtraction(ctx, "not-a-uuid", 1, sentences); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("malformed id err = %v, want ErrDocumentNotFound", err)
	}

	doc := createTestDocument(ctx, t, store, "Once")
	if _, err := store.StoreDocumentExtraction(ctx, doc.ID, 1, sentences); err != nil {
		t.Fatalf("first extraction: %v", err)
	}
	if _, err := store.StoreDocumentExtraction(ctx, doc.ID, 1, sentences); !errors.Is(err, domain.ErrDocumentNotPending) {
		t.Errorf("second extraction err = %v, want ErrDocumentNotPending", err)
	}
}

// TestStoreDocumentExtractionAtomic proves a failing sentence insert rolls the
// whole extraction back: the document stays pending and stores no sentences, so
// a rejected extraction leaves no orphaned rows.
func TestStoreDocumentExtractionAtomic(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := createTestDocument(ctx, t, store, "Broken")
	dup := []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
		{Seq: 0, Page: 1, Text: "Doublon.", Occurrence: 1},
	}
	if _, err := store.StoreDocumentExtraction(ctx, doc.ID, 1, dup); err == nil {
		t.Fatal("duplicate seq accepted")
	}

	got, err := store.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.Status != domain.DocumentStatusPending {
		t.Errorf("status = %q, want pending after rollback", got.Status)
	}
	stored, err := store.ListDocumentSentences(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentSentences: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("stored %d sentences after rollback, want 0", len(stored))
	}
}

// seedTestClaim inserts one document_claims row directly; the write path is the
// analyzer card's, but the read path (list counts, claims endpoint) ships here.
func seedTestClaim(ctx context.Context, t *testing.T, store *Store, docID string, seq int, claimID, verdict, citations string) {
	t.Helper()
	_, err := store.pool.Exec(ctx, `
		INSERT INTO document_claims (document_id, sentence_seq, claim_id, text, status, source, verdict, basis, flags, confidence, rationale, citations)
		VALUES ($1, $2, $3, 'claim text', 'verified', 'verified', $4, 'evidence', '{"missing-context"}', 0.8, 'rationale', $5)`,
		docID, seq, claimID, verdict, citations)
	if err != nil {
		t.Fatalf("seed claim: %v", err)
	}
}

func TestListDocumentsCountsAndOrder(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	older := createTestDocument(ctx, t, store, "Older")
	newer := createTestDocument(ctx, t, store, "Newer")
	sentences := []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "Deux.", Occurrence: 1},
	}
	// Both documents complete extraction so they are ready library rows; a
	// pending document is excluded from the list (asserted separately).
	if _, err := store.StoreDocumentExtraction(ctx, older.ID, 1, sentences); err != nil {
		t.Fatalf("extraction: %v", err)
	}
	if _, err := store.StoreDocumentExtraction(ctx, newer.ID, 1, sentences); err != nil {
		t.Fatalf("extraction newer: %v", err)
	}
	seedTestClaim(ctx, t, store, older.ID, 0, "c-1", "credible", "[]")
	seedTestClaim(ctx, t, store, older.ID, 0, "c-2", "credible", "[]")
	seedTestClaim(ctx, t, store, older.ID, 1, "c-3", "disputed", "[]")
	seedTestClaim(ctx, t, store, older.ID, 1, "c-4", "unverifiable", "[]")

	// Backdate the older document so the newest-first ordering is asserted
	// deterministically instead of trusting insert-time clock ticks.
	if _, err := store.pool.Exec(ctx, "UPDATE documents SET created_at = created_at - interval '1 hour' WHERE id = $1", older.ID); err != nil {
		t.Fatalf("backdate older: %v", err)
	}

	list, err := store.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d documents, want 2", len(list))
	}
	if list[0].ID != newer.ID || list[1].ID != older.ID {
		t.Errorf("order = [%s, %s], want newest first", list[0].ID, list[1].ID)
	}
	got := list[1]
	if got.CredibleClaims != 2 || got.DisputedClaims != 1 {
		t.Errorf("counts = %d credible / %d disputed, want 2/1 (unverifiable never counted)", got.CredibleClaims, got.DisputedClaims)
	}
	if list[0].CredibleClaims != 0 || list[0].DisputedClaims != 0 {
		t.Errorf("claimless counts = %d/%d, want 0/0", list[0].CredibleClaims, list[0].DisputedClaims)
	}
}

func TestListDocumentsExcludesPending(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// A pending document is an upload whose extraction was never ingested (an
	// over-cap rejection or an abandoned upload); it must not surface in the
	// library as a permanent "Pending" ghost card.
	pending := createTestDocument(ctx, t, store, "Pending")
	ready := createTestDocument(ctx, t, store, "Ready")
	if _, err := store.StoreDocumentExtraction(ctx, ready.ID, 1, []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
	}); err != nil {
		t.Fatalf("extraction: %v", err)
	}

	list, err := store.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d documents, want 1 (pending excluded)", len(list))
	}
	if list[0].ID != ready.ID {
		t.Errorf("listed id = %s, want the ready document %s", list[0].ID, ready.ID)
	}
	for _, item := range list {
		if item.ID == pending.ID {
			t.Errorf("pending document %s appeared in the library list", pending.ID)
		}
	}
}

func TestListDocumentClaimsRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := createTestDocument(ctx, t, store, "Claims")
	sentences := []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
		{Seq: 1, Page: 1, Text: "Deux.", Occurrence: 1},
	}
	if _, err := store.StoreDocumentExtraction(ctx, doc.ID, 1, sentences); err != nil {
		t.Fatalf("extraction: %v", err)
	}
	citations := `[{"kind":"claim","claim":"X","verdict":"corroborates","sources":[{"title":"T","url":"https://t.example"}],"similarity":0.91,"contribution":1,"evidence_id":"ev-1"}]`
	// Insert out of document order to prove the read reorders by sentence.
	seedTestClaim(ctx, t, store, doc.ID, 1, "c-later", "disputed", "[]")
	seedTestClaim(ctx, t, store, doc.ID, 0, "c-first", "credible", citations)

	claims, err := store.ListDocumentClaims(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentClaims: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("listed %d claims, want 2", len(claims))
	}
	first := claims[0]
	if first.SentenceSeq != 0 || first.ClaimID != "c-first" {
		t.Fatalf("claims not in document order: %+v", claims)
	}
	if first.ID == "" || first.DocumentID != doc.ID || first.CreatedAt.IsZero() {
		t.Errorf("claim identity incomplete: %+v", first)
	}
	if first.Status != domain.DocumentClaimVerified || first.Verdict != "credible" || first.Basis != "evidence" {
		t.Errorf("claim verdict mismatch: %+v", first)
	}
	if len(first.Flags) != 1 || first.Flags[0] != "missing-context" {
		t.Errorf("flags = %v, want [missing-context]", first.Flags)
	}
	if first.Confidence != 0.8 || first.Rationale != "rationale" {
		t.Errorf("confidence/rationale mismatch: %+v", first)
	}
	if len(first.Citations) != 1 {
		t.Fatalf("citations = %+v, want one match", first.Citations)
	}
	c := first.Citations[0]
	if c.Claim != "X" || c.Verdict != domain.VerdictCorroborates || c.EvidenceID != "ev-1" || c.Similarity != 0.91 {
		t.Errorf("citation round trip mismatch: %+v", c)
	}
	if len(c.Sources) != 1 || c.Sources[0].URL != "https://t.example" {
		t.Errorf("citation sources mismatch: %+v", c.Sources)
	}

	empty, err := store.ListDocumentClaims(ctx, uuid.NewString())
	if err != nil {
		t.Fatalf("ListDocumentClaims (absent): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("absent document claims = %+v, want empty", empty)
	}
}

// TestListDocumentClaimsSameTransactionOrder proves a sentence's claims come
// back in insertion order even when they share one created_at, the shape an
// analysis run produces: now() is transaction-stable, so the ordinal identity,
// not the timestamp, must carry the order.
func TestListDocumentClaimsSameTransactionOrder(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := createTestDocument(ctx, t, store, "Ordered")
	if _, err := store.StoreDocumentExtraction(ctx, doc.ID, 1, []domain.DocumentSentence{
		{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1},
	}); err != nil {
		t.Fatalf("extraction: %v", err)
	}
	// One statement = one transaction = identical created_at for both rows.
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO document_claims (document_id, sentence_seq, claim_id, text, status, verdict, citations)
		VALUES ($1, 0, 'c-first', 'first', 'verified', 'credible', '[]'),
		       ($1, 0, 'c-second', 'second', 'verified', 'disputed', '[]')`, doc.ID); err != nil {
		t.Fatalf("seed claims: %v", err)
	}

	claims, err := store.ListDocumentClaims(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentClaims: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("listed %d claims, want 2", len(claims))
	}
	if claims[0].ClaimID != "c-first" || claims[1].ClaimID != "c-second" {
		t.Errorf("order = [%s, %s], want insertion order", claims[0].ClaimID, claims[1].ClaimID)
	}
	if !claims[0].CreatedAt.Equal(claims[1].CreatedAt) {
		t.Fatalf("test premise broken: created_at values differ (%v vs %v)", claims[0].CreatedAt, claims[1].CreatedAt)
	}
}

func TestDeleteDocumentCascades(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := createTestDocument(ctx, t, store, "Doomed")
	if _, err := store.StoreDocumentExtraction(ctx, doc.ID, 1, []domain.DocumentSentence{{Seq: 0, Page: 1, Text: "Une.", Occurrence: 1}}); err != nil {
		t.Fatalf("extraction: %v", err)
	}
	seedTestClaim(ctx, t, store, doc.ID, 0, "c-1", "credible", "[]")

	if err := store.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if _, err := store.GetDocument(ctx, doc.ID); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("document survives delete: err = %v", err)
	}
	var sentences, claims int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM document_sentences WHERE document_id = $1", doc.ID).Scan(&sentences); err != nil {
		t.Fatalf("count sentences: %v", err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM document_claims WHERE document_id = $1", doc.ID).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if sentences != 0 || claims != 0 {
		t.Errorf("cascade left %d sentences, %d claims", sentences, claims)
	}

	if err := store.DeleteDocument(ctx, doc.ID); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("second delete err = %v, want ErrDocumentNotFound", err)
	}
	if err := store.DeleteDocument(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("malformed id err = %v, want ErrDocumentNotFound", err)
	}
}
