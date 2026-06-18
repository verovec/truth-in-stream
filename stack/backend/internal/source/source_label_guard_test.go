package source_test

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// TestSourceLabelCoversEveryEvidenceKind guards the lockstep between the source
// kinds the retrieval layer mints into evidence ids and the French labels the
// domain layer derives from them. domain.WinningSource cannot import this
// package (it is the lowest layer and matches on the kind string), so this
// cross-package test is what catches a kind being renamed in one place but not
// the other: a renamed kind would build an evidence id the domain mapping no
// longer recognizes, dropping the chip silently in production. KindStats is the
// adapter-level family and is never minted into an evidence id (the per-provider
// KindStatsINSEE / KindStatsEurostat are), so it is intentionally excluded.
func TestSourceLabelCoversEveryEvidenceKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind       source.Kind
		provenance string
		want       string
	}{
		{source.KindStatsINSEE, "INSEE", domain.SourceLabelINSEE},
		{source.KindStatsEurostat, "Eurostat", domain.SourceLabelEurostat},
		{source.KindWebSearch, "lemonde.fr", domain.SourceLabelWeb},
		{source.KindVotingRecord, "Assemblee nationale (scrutin)", domain.SourceLabelAssemblee},
		{source.KindVotingRecord, "Senat (scrutin)", domain.SourceLabelSenat},
		{source.KindAttribution, "Le Monde", "Le Monde"},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind)+"/"+tt.provenance, func(t *testing.T) {
			t.Parallel()
			id := source.NewEvidenceID(tt.kind, "sid", 0).String()
			match := domain.SegmentMatch{
				EvidenceID: id,
				Sources:    []domain.Source{{Title: tt.provenance, URL: "https://example.test/x"}},
			}
			if got := domain.WinningSource([]domain.SegmentMatch{match}); got.Label != tt.want {
				t.Errorf("WinningSource label for kind %q = %q, want %q", tt.kind, got.Label, tt.want)
			}
		})
	}
}
