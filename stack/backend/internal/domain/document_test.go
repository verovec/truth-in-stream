package domain_test

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestDocumentStatusValid(t *testing.T) {
	t.Parallel()
	for _, s := range []domain.DocumentStatus{domain.DocumentStatusPending, domain.DocumentStatusReady, domain.DocumentStatusFailed} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []domain.DocumentStatus{"", "uploaded", "PENDING"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestDocumentAnalysisStatusValid(t *testing.T) {
	t.Parallel()
	for _, s := range []domain.DocumentAnalysisStatus{domain.DocumentAnalysisNone, domain.DocumentAnalysisAnalysing, domain.DocumentAnalysisComplete, domain.DocumentAnalysisFailed} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []domain.DocumentAnalysisStatus{"", "running", "done"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestDocumentClaimStatusValid(t *testing.T) {
	t.Parallel()
	for _, s := range []domain.DocumentClaimStatus{domain.DocumentClaimVerified, domain.DocumentClaimError} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []domain.DocumentClaimStatus{"", "unchecked", "pending"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

// TestDocumentObjectKey pins the storage layout the design fixes: one folder
// per document under the documents/ prefix, leaving room for derived assets.
func TestDocumentObjectKey(t *testing.T) {
	t.Parallel()
	got := domain.DocumentObjectKey("123e4567-e89b-12d3-a456-426614174000")
	want := "documents/123e4567-e89b-12d3-a456-426614174000/original.pdf"
	if got != want {
		t.Errorf("DocumentObjectKey = %q, want %q", got, want)
	}
}
