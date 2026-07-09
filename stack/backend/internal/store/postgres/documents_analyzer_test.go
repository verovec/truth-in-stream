package postgres

import (
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// readyDocument creates a document and drives it to ready with n sentences,
// the state an analysis run starts from.
func readyDocument(t *testing.T, store *Store, n int) domain.Document {
	t.Helper()
	ctx := t.Context()
	doc := createTestDocument(ctx, t, store, "Analyzable")
	sentences := make([]domain.DocumentSentence, n)
	for i := range sentences {
		sentences[i] = domain.DocumentSentence{Seq: i, Page: 1, Text: "Phrase.", Occurrence: i + 1}
	}
	ready, err := store.StoreDocumentExtraction(ctx, doc.ID, 1, sentences)
	if err != nil {
		t.Fatalf("StoreDocumentExtraction: %v", err)
	}
	return ready
}

func sampleClaim(docID string, seq int, claimID string) domain.DocumentClaim {
	return domain.DocumentClaim{
		DocumentID: docID, SentenceSeq: seq, ClaimID: claimID, Text: "claim",
		Status: domain.DocumentClaimVerified, Source: "verified", Verdict: "credible",
		Basis: "evidence", Flags: []string{}, Confidence: 0.8, Rationale: "solide",
		Citations: []domain.SegmentMatch{},
	}
}

func TestStartDocumentAnalysisWipesAndLocks(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := readyDocument(t, store, 2)
	// Seed a prior run's state: a skip reason and a claim.
	if err := store.RecordDocumentSentenceResult(ctx, doc.ID, 0, domain.SkipReasonNotAClaim, nil); err != nil {
		t.Fatalf("seed skip: %v", err)
	}
	if err := store.RecordDocumentSentenceResult(ctx, doc.ID, 1, domain.SkipReasonNone, []domain.DocumentClaim{sampleClaim(doc.ID, 1, "c-old")}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	locked, err := store.StartDocumentAnalysis(ctx, doc.ID)
	if err != nil {
		t.Fatalf("StartDocumentAnalysis: %v", err)
	}
	if locked.AnalysisStatus != domain.DocumentAnalysisAnalysing {
		t.Errorf("analysis status = %q, want analysing", locked.AnalysisStatus)
	}
	if locked.SentencesProcessed != 0 {
		t.Errorf("sentences_processed = %d, want 0 after reset", locked.SentencesProcessed)
	}

	claims, err := store.ListDocumentClaims(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentClaims: %v", err)
	}
	if len(claims) != 0 {
		t.Errorf("claims not wiped: %+v", claims)
	}
	sentences, err := store.ListDocumentSentences(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentSentences: %v", err)
	}
	for _, s := range sentences {
		if s.SkipReason != domain.SkipReasonNone {
			t.Errorf("sentence %d skip reason %q not cleared", s.Seq, s.SkipReason)
		}
	}
}

func TestStartDocumentAnalysisGuards(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if _, err := store.StartDocumentAnalysis(ctx, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("unknown id err = %v, want ErrDocumentNotFound", err)
	}
	if _, err := store.StartDocumentAnalysis(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("malformed id err = %v, want ErrDocumentNotFound", err)
	}

	pending := createTestDocument(ctx, t, store, "Pending")
	if _, err := store.StartDocumentAnalysis(ctx, pending.ID); !errors.Is(err, domain.ErrDocumentNotReady) {
		t.Errorf("pending doc err = %v, want ErrDocumentNotReady", err)
	}

	doc := readyDocument(t, store, 1)
	if _, err := store.StartDocumentAnalysis(ctx, doc.ID); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if _, err := store.StartDocumentAnalysis(ctx, doc.ID); !errors.Is(err, domain.ErrAnalysisInProgress) {
		t.Errorf("concurrent start err = %v, want ErrAnalysisInProgress", err)
	}
}

func TestRecordDocumentSentenceResult(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := readyDocument(t, store, 2)
	if _, err := store.StartDocumentAnalysis(ctx, doc.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Sentence 0: a skip.
	if err := store.RecordDocumentSentenceResult(ctx, doc.ID, 0, domain.SkipReasonNotCovered, nil); err != nil {
		t.Fatalf("record skip: %v", err)
	}
	// Sentence 1: two claims, in order.
	claims := []domain.DocumentClaim{sampleClaim(doc.ID, 1, "c-1"), sampleClaim(doc.ID, 1, "c-2")}
	if err := store.RecordDocumentSentenceResult(ctx, doc.ID, 1, domain.SkipReasonNone, claims); err != nil {
		t.Fatalf("record claims: %v", err)
	}

	got, err := store.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.SentencesProcessed != 2 {
		t.Errorf("sentences_processed = %d, want 2", got.SentencesProcessed)
	}
	sentences, err := store.ListDocumentSentences(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentSentences: %v", err)
	}
	if sentences[0].SkipReason != domain.SkipReasonNotCovered {
		t.Errorf("sentence 0 skip = %q, want not_covered", sentences[0].SkipReason)
	}
	stored, err := store.ListDocumentClaims(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentClaims: %v", err)
	}
	if len(stored) != 2 || stored[0].ClaimID != "c-1" || stored[1].ClaimID != "c-2" {
		t.Errorf("claims = %+v, want c-1 then c-2 in insertion order", stored)
	}
}

func TestCompleteAndFailDocumentAnalysis(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	doc := readyDocument(t, store, 1)
	if _, err := store.StartDocumentAnalysis(ctx, doc.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := store.CompleteDocumentAnalysis(ctx, doc.ID); err != nil {
		t.Fatalf("CompleteDocumentAnalysis: %v", err)
	}
	got, err := store.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.AnalysisStatus != domain.DocumentAnalysisComplete || got.AnalysisRuns != 1 || got.AnalyzedAt.IsZero() {
		t.Errorf("after complete = %+v, want complete/runs=1/analyzed_at set", got)
	}

	// A second run then a failure.
	if _, err := store.StartDocumentAnalysis(ctx, doc.ID); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if err := store.FailDocumentAnalysis(ctx, doc.ID, "boom"); err != nil {
		t.Fatalf("FailDocumentAnalysis: %v", err)
	}
	got, err = store.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.AnalysisStatus != domain.DocumentAnalysisFailed || got.AnalysisError != "boom" {
		t.Errorf("after fail = %+v, want failed/boom", got)
	}
	if got.AnalysisRuns != 1 {
		t.Errorf("analysis_runs = %d, want still 1 (a failed run does not count)", got.AnalysisRuns)
	}
}

func TestRecoverInterruptedAnalyses(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	stuck := readyDocument(t, store, 1)
	if _, err := store.StartDocumentAnalysis(ctx, stuck.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	idle := readyDocument(t, store, 1) // ready, analysis none - must be untouched.

	recovered, err := store.RecoverInterruptedAnalyses(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedAnalyses: %v", err)
	}
	if len(recovered) != 1 || recovered[0] != stuck.ID {
		t.Errorf("recovered = %v, want [%s]", recovered, stuck.ID)
	}

	got, err := store.GetDocument(ctx, stuck.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.AnalysisStatus != domain.DocumentAnalysisFailed || got.AnalysisError == "" {
		t.Errorf("stuck doc = %+v, want failed with an error", got)
	}

	idleGot, err := store.GetDocument(ctx, idle.ID)
	if err != nil {
		t.Fatalf("GetDocument idle: %v", err)
	}
	if idleGot.AnalysisStatus != domain.DocumentAnalysisNone {
		t.Errorf("idle doc analysis status = %q, want none (untouched)", idleGot.AnalysisStatus)
	}
}
