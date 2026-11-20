package service

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// agedCuratedMatch is a perfect-similarity curated hit checked at the given
// time, so only the age guard decides whether it is borrowed.
func agedCuratedMatch(checkedAt time.Time) domain.PoliticalClaimMatch {
	return domain.PoliticalClaimMatch{
		ID:             "aged-1",
		Text:           "Le deficit public a atteint 5,5% du PIB en 2023",
		LiteralVerdict: domain.LiteralAccurate,
		SourceName:     "INSEE",
		SourceURL:      "https://www.insee.fr/fr/statistiques/x",
		QuotedSpan:     "5,5% du PIB en 2023.",
		CheckedAt:      checkedAt,
		Distance:       0,
	}
}

func agedVerifyPath(store PoliticalClaimSearcher, maxAge time.Duration) *VerifyPath {
	return &VerifyPath{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		pol:    &PoliticalConfig{CuratedStore: store, CuratedTau: 0.85, CuratedMaxAge: maxAge},
	}
}

func TestPoliticalFastMatchAgeGuard(t *testing.T) {
	t.Parallel()
	year := 365 * 24 * time.Hour

	tests := []struct {
		name      string
		checkedAt time.Time
		maxAge    time.Duration
		borrow    bool
	}{
		{"fresh verdict borrowed", time.Now().Add(-24 * time.Hour), year, true},
		{"stale verdict falls through", time.Now().Add(-2 * 365 * 24 * time.Hour), year, false},
		{"undated verdict stays borrowable", time.Time{}, year, true},
		{"guard off keeps stale borrowable", time.Now().Add(-10 * 365 * 24 * time.Hour), 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &stubPoliticalClaimSearcher{matches: []domain.PoliticalClaimMatch{agedCuratedMatch(tc.checkedAt)}}
			vp := agedVerifyPath(store, tc.maxAge)

			verdict, ok := vp.politicalFastMatch(context.Background(), make([]float32, domain.EmbeddingDim))
			if ok != tc.borrow {
				t.Fatalf("borrow = %v, want %v", ok, tc.borrow)
			}
			if tc.borrow && verdict == nil {
				t.Fatal("borrowed but verdict is nil")
			}
		})
	}
}
