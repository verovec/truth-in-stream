package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
)

// gateFunc adapts a plain function to the Gate interface so a test can fake a
// fact-checkability judgment without an LLM.
type gateFunc func(ctx context.Context, passage string) (bool, error)

func (f gateFunc) FactCheckable(ctx context.Context, passage string) (bool, error) {
	return f(ctx, passage)
}

type fakeSource struct {
	members []CategoryMember
	lead    map[string]Extract
	full    map[string]Extract
}

func (f fakeSource) CategoryMembers(_ context.Context, _ []string, _, _ int) ([]CategoryMember, error) {
	return f.members, nil
}

func (f fakeSource) Extracts(_ context.Context, titles []string) ([]Extract, error) {
	return collect(f.lead, titles), nil
}

func (f fakeSource) FullExtracts(_ context.Context, titles []string) ([]Extract, error) {
	return collect(f.full, titles), nil
}

func collect(m map[string]Extract, titles []string) []Extract {
	out := make([]Extract, 0, len(titles))
	for _, t := range titles {
		if e, ok := m[t]; ok {
			out = append(out, e)
		}
	}
	return out
}

type capturePublisher struct {
	mu   sync.Mutex
	jobs []crawljob.CrawlJob
	prio []uint8
}

func (p *capturePublisher) Publish(_ context.Context, body []byte, priority uint8) error {
	var j crawljob.CrawlJob
	if err := json.Unmarshal(body, &j); err != nil {
		return err
	}
	p.mu.Lock()
	p.jobs = append(p.jobs, j)
	p.prio = append(p.prio, priority)
	p.mu.Unlock()
	return nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRunCrawlPublishesLeadAndBody(t *testing.T) {
	t.Parallel()
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Atom"}},
		lead:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter."}},
		full:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter.\n\nAtoms bond into molecules."}},
	}
	pub := &capturePublisher{}
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, nil, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "simplewiki-crawl", Project: "simplewiki",
		MaxDepth: 1, MaxPages: 100, IncludeBody: true, MaxPriority: 10,
	})
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if stats.Published != len(pub.jobs) || stats.Published == 0 {
		t.Fatalf("published = %d, jobs = %d", stats.Published, len(pub.jobs))
	}
	kinds := make([]string, 0, len(pub.jobs))
	for _, j := range pub.jobs {
		if j.PageID != 10 || j.Corpus != "simplewiki-crawl" || j.RevisionID != 99 {
			t.Errorf("job metadata wrong: %+v", j)
		}
		if j.URL != "https://simple.wikipedia.org/wiki/Atom" {
			t.Errorf("job url = %q", j.URL)
		}
		kinds = append(kinds, j.Kind)
	}
	if !contains(kinds, "lead") || !contains(kinds, "body") {
		t.Errorf("kinds = %v, want both lead and body", kinds)
	}
	// chunk_index is contiguous from 0 with no gaps or dups.
	seen := map[int]bool{}
	for _, j := range pub.jobs {
		if seen[j.ChunkIndex] {
			t.Errorf("duplicate chunk_index %d", j.ChunkIndex)
		}
		seen[j.ChunkIndex] = true
	}
	for i := range pub.jobs {
		if !seen[i] {
			t.Errorf("missing chunk_index %d", i)
		}
	}
}

func TestRunCrawlLeadPriorityAboveBody(t *testing.T) {
	t.Parallel()
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Atom"}},
		lead:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter."}},
		full:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter.\n\nAtoms bond into molecules."}},
	}
	pub := &capturePublisher{}
	if _, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, nil, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, IncludeBody: true, MaxPriority: 10,
	}); err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	for i, j := range pub.jobs {
		switch j.Kind {
		case "lead":
			if pub.prio[i] != 10 {
				t.Errorf("lead priority = %d, want 10", pub.prio[i])
			}
		case "body":
			if pub.prio[i] != 5 {
				t.Errorf("body priority = %d, want 5", pub.prio[i])
			}
		}
	}
}

func TestRunCrawlLeadOnly(t *testing.T) {
	t.Parallel()
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Atom"}},
		lead:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Atom is matter."}},
	}
	pub := &capturePublisher{}
	if _, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, nil, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, IncludeBody: false, MaxPriority: 10,
	}); err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if len(pub.jobs) == 0 {
		t.Fatal("no jobs published")
	}
	for _, j := range pub.jobs {
		if j.Kind != "lead" {
			t.Errorf("kind = %q, want lead only", j.Kind)
		}
	}
}

func TestRunCrawlSkipsMissing(t *testing.T) {
	t.Parallel()
	src := fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Gone"}},
		lead:    map[string]Extract{"Gone": {Title: "Gone", Missing: true}},
	}
	pub := &capturePublisher{}
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, nil, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, IncludeBody: false, MaxPriority: 10,
	})
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if stats.Published != 0 {
		t.Errorf("published = %d, want 0 for a missing page", stats.Published)
	}
}

func TestRunCrawlRejectsZeroPriority(t *testing.T) {
	t.Parallel()
	if _, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), fakeSource{}, &capturePublisher{}, nil, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki", MaxPriority: 0,
	}); err == nil {
		t.Fatal("RunCrawl with zero MaxPriority = nil error, want error")
	}
}

// gatedSource is a single page whose lead and body carry distinct marker words,
// so a fake gate can keep one kind and drop the other.
func gatedSource() fakeSource {
	return fakeSource{
		members: []CategoryMember{{PageID: 10, Title: "Atom"}},
		lead:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Keepme is a verifiable fact about matter."}},
		full:    map[string]Extract{"Atom": {PageID: 10, Title: "Atom", RevisionID: 99, Text: "Keepme is a verifiable fact about matter.\n\nDropme is navigational filler prose."}},
	}
}

func gatedCrawlConfig() CrawlConfig {
	return CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki",
		MaxPages: 100, IncludeBody: true, MaxPriority: 10, GateConcurrency: 4,
	}
}

func TestRunCrawlGateDropsNonFactCheckable(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	// The gate keeps only passages mentioning the lead marker; body chunks drop.
	gate := gateFunc(func(_ context.Context, passage string) (bool, error) {
		return strings.Contains(passage, "Keepme"), nil
	})
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), pub, gate, gatedCrawlConfig())
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if stats.Dropped == 0 {
		t.Fatal("expected at least one dropped body chunk")
	}
	if stats.Published != len(pub.jobs) {
		t.Errorf("published = %d, jobs = %d", stats.Published, len(pub.jobs))
	}
	for _, j := range pub.jobs {
		if j.Kind != "lead" {
			t.Errorf("published a %q chunk, want only fact-checkable lead chunks", j.Kind)
		}
		if !strings.Contains(j.Content, "Keepme") {
			t.Errorf("published a chunk the gate should have dropped: %q", j.Content)
		}
	}
}

func TestRunCrawlGateFailOpenOnError(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	// Every gate call errors; fail-open means every chunk is still published.
	gate := gateFunc(func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("model unavailable")
	})
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), pub, gate, gatedCrawlConfig())
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if stats.Dropped != 0 {
		t.Errorf("dropped = %d, want 0 (gate errors fail open)", stats.Dropped)
	}
	if stats.Published == 0 || stats.Published != len(pub.jobs) {
		t.Fatalf("published = %d, jobs = %d; a failing gate must publish every chunk", stats.Published, len(pub.jobs))
	}
}

func TestRunCrawlNilGatePublishesEverything(t *testing.T) {
	t.Parallel()
	// A nil gate is CRAWL_CHECKWORTHY=false: publish all, drop nothing.
	withGate := &capturePublisher{}
	gate := gateFunc(func(_ context.Context, passage string) (bool, error) {
		return strings.Contains(passage, "Keepme"), nil
	})
	if _, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), withGate, gate, gatedCrawlConfig()); err != nil {
		t.Fatalf("RunCrawl gated: %v", err)
	}
	noGate := &capturePublisher{}
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), noGate, nil, gatedCrawlConfig())
	if err != nil {
		t.Fatalf("RunCrawl nil gate: %v", err)
	}
	if stats.Dropped != 0 {
		t.Errorf("nil-gate dropped = %d, want 0", stats.Dropped)
	}
	if len(noGate.jobs) <= len(withGate.jobs) {
		t.Errorf("nil gate published %d, gated published %d; nil gate must publish at least as many",
			len(noGate.jobs), len(withGate.jobs))
	}
}

func TestRunCrawlGateRPMPacedStillCorrect(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	var calls atomic.Int64
	gate := gateFunc(func(_ context.Context, _ string) (bool, error) {
		calls.Add(1)
		return true, nil
	})
	cfg := gatedCrawlConfig()
	cfg.GateRPM = 6000 // high enough that pacing never slows the test materially
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), gatedSource(), pub, gate, cfg)
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if calls.Load() == 0 {
		t.Fatal("expected the gate to be called")
	}
	if stats.Published != len(pub.jobs) || stats.Dropped != 0 {
		t.Errorf("published = %d, jobs = %d, dropped = %d", stats.Published, len(pub.jobs), stats.Dropped)
	}
}

func TestBodyTextStripsLeadPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		full, lead string
		want       string
	}{
		{"clean prefix", "Lead text.\n\nBody text.", "Lead text.", "Body text."},
		{"not a prefix returns full", "Totally different.", "Lead text.", "Totally different."},
		{"empty lead returns full", "Body only.", "", "Body only."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bodyText(tc.full, tc.lead); got != tc.want {
				t.Errorf("bodyText(%q, %q) = %q, want %q", tc.full, tc.lead, got, tc.want)
			}
		})
	}
}
