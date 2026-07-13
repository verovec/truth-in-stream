package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// gaugeVerifier records the peak number of verifier calls in flight at once. It
// blocks each call until `target` of them are simultaneously in flight, so a run
// over enough claims proves the verify pool lets exactly its concurrency run
// concurrently - the throughput ceiling the pool defaults set.
type gaugeVerifier struct {
	target  int
	mu      sync.Mutex
	cur     int
	max     int
	reached chan struct{}
	closed  bool
}

func newGaugeVerifier(target int) *gaugeVerifier {
	return &gaugeVerifier{target: target, reached: make(chan struct{})}
}

func (g *gaugeVerifier) Verify(ctx context.Context, _ string, _ []EvidencePassage) (ClaimVerdict, error) {
	g.mu.Lock()
	g.cur++
	if g.cur > g.max {
		g.max = g.cur
	}
	if g.cur == g.target && !g.closed {
		g.closed = true
		close(g.reached)
	}
	g.mu.Unlock()

	// Hold the slot until `target` calls overlap (or ctx dies), so the peak count
	// reflects the pool's true concurrency rather than a scheduling accident.
	select {
	case <-g.reached:
	case <-ctx.Done():
		return ClaimVerdict{}, ctx.Err()
	}

	g.mu.Lock()
	g.cur--
	g.mu.Unlock()
	return ClaimVerdict{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.7}, nil
}

func (g *gaugeVerifier) peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.max
}

// TestVerifyPoolConcurrencyIsTheThroughputCeiling is the offline load check
// backing the verify-pool default review (VER-202): the pool caps in-flight
// verifier calls at exactly its concurrency, so throughput under sustained speech
// scales with that knob and nothing else. The default stays 2 (kept, not raised)
// because raising it trades LLM cost and provider rate-limit headroom that only a
// live load test against real latency can size; the knob is now env-tunable
// (FACTCHECK_VERIFY_CONCURRENCY, forwarded through compose) for that tuning.
func TestVerifyPoolConcurrencyIsTheThroughputCeiling(t *testing.T) {
	t.Parallel()
	for _, concurrency := range []int{1, 2, 4} {
		t.Run("concurrency_"+itoa(concurrency), func(t *testing.T) {
			t.Parallel()
			const claims = 8
			claimTexts := make([]string, claims)
			byText := make(map[string][]string, 1)
			decomp := make([]string, claims)
			matches := make(map[string][]domain.SegmentMatch, claims)
			for i := range claimTexts {
				id := "claim-" + itoa(i)
				decomp[i] = id
				matches[id] = []domain.SegmentMatch{{Kind: domain.MatchKindEvidence, Claim: "evidence", EvidenceID: "evidence:" + itoa(i) + ":0", Similarity: 0.6}}
			}
			byText["unit"] = decomp

			verifier := newGaugeVerifier(concurrency)
			vp, err := NewVerifyPath(VerifyPathConfig{
				Decomposer:        fakeDecomposer{byText: byText},
				Matcher:           liveMatcher{matches: matches},
				Verifier:          verifier,
				FastTau:           0.95, // above the evidence similarity so nothing is borrowed
				VerifyConcurrency: concurrency,
				VerifyQueueDepth:  0,
				FastDeadline:      time.Second,
				VerifyDeadline:    2 * time.Second,
				Logger:            discardLogger(),
			})
			if err != nil {
				t.Fatalf("NewVerifyPath: %v", err)
			}

			res, err := vp.AnalyzeText(t.Context(), allowAllPrechecker{}, "unit", "anchor")
			if err != nil {
				t.Fatalf("AnalyzeText: %v", err)
			}
			if len(res.Claims) != claims {
				t.Fatalf("resolved %d claims, want %d", len(res.Claims), claims)
			}
			for _, c := range res.Claims {
				if c.Status != ClaimStatusVerified {
					t.Fatalf("batch claim status = %q, want verified (batch never sheds)", c.Status)
				}
			}
			if got := verifier.peak(); got != concurrency {
				t.Errorf("peak in-flight verifier calls = %d, want exactly the pool concurrency %d", got, concurrency)
			}
		})
	}
}

// itoa is a tiny non-negative int formatter kept local so this test needs no fmt
// for its subtest names.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
