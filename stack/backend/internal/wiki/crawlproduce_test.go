package wiki

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
)

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
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "simplewiki-crawl", Project: "simplewiki",
		MaxDepth: 1, MaxPages: 100, IncludeBody: true, MaxPriority: 10,
	})
	if err != nil {
		t.Fatalf("RunCrawl: %v", err)
	}
	if stats.Published != len(pub.jobs) || stats.Published == 0 {
		t.Fatalf("published = %d, jobs = %d", stats.Published, len(pub.jobs))
	}
	var kinds []string
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
	if _, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, CrawlConfig{
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
	if _, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, CrawlConfig{
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
	stats, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), src, pub, CrawlConfig{
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
	if _, err := RunCrawl(t.Context(), slog.New(slog.DiscardHandler), fakeSource{}, &capturePublisher{}, CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "c", Project: "simplewiki", MaxPriority: 0,
	}); err == nil {
		t.Fatal("RunCrawl with zero MaxPriority = nil error, want error")
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
