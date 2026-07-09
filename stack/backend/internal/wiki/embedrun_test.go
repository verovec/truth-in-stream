package wiki

import (
	"context"
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// discardLogger silences a run's logs in tests that assert on behavior rather
// than on the log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func contentFor(page, idx int) string {
	return "page-" + string(rune('A'+page)) + "-chunk-" + string(rune('0'+idx))
}

// fakeEmbedSource serves a fixed remaining count to the dry-run estimate.
type fakeEmbedSource struct {
	remaining domain.EvidenceRemaining
}

func (f fakeEmbedSource) StagingRemaining(context.Context) (domain.EvidenceRemaining, error) {
	return f.remaining, nil
}

func TestEstimateBulkEmbed(t *testing.T) {
	t.Parallel()
	src := fakeEmbedSource{remaining: domain.EvidenceRemaining{Documents: 10, Chunks: 100, Chars: 5000}}

	est, err := EstimateBulkEmbed(t.Context(), src)
	if err != nil {
		t.Fatalf("EstimateBulkEmbed: %v", err)
	}
	// 5000 chars / 5 chars-per-token = 1000 tokens; 1000/1e6 * $0.12 = $1.2e-4.
	if est.Pages != 10 || est.Chunks != 100 || est.Tokens != 1000 {
		t.Errorf("estimate counts = %+v, want pages 10 chunks 100 tokens 1000", est)
	}
	if est.CostUSD < 1.19e-4 || est.CostUSD > 1.21e-4 {
		t.Errorf("cost = %v, want ~1.2e-4", est.CostUSD)
	}
}
