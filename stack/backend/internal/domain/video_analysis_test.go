package domain_test

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestVideoAnalysisStatusValid(t *testing.T) {
	t.Parallel()
	for _, s := range []domain.VideoAnalysisStatus{domain.VideoAnalysisNone, domain.VideoAnalysisAnalysing, domain.VideoAnalysisComplete, domain.VideoAnalysisFailed} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []domain.VideoAnalysisStatus{"", "running", "done", "COMPLETE"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}
