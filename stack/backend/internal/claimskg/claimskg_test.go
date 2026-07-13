package claimskg

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
)

type recordingPublisher struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (p *recordingPublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies = append(p.bodies, body)
	return nil
}

func (p *recordingPublisher) jobs(t *testing.T) []factcheckjob.ClaimJob {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]factcheckjob.ClaimJob, 0, len(p.bodies))
	for _, b := range p.bodies {
		var j factcheckjob.ClaimJob
		if err := json.Unmarshal(b, &j); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, j)
	}
	return out
}

const seedCSV = `claimReviewed,rating,claimReview_url,claimReview_author_name,claimReview_datePublished
"La Terre est plate","FALSE","https://www.snopes.com/fact-check/flat-earth","Snopes","2021-03-04"
"Vaccine microchips","MIXTURE","https://factuel.afp.com/vaccins","AFP","2021-05-05"
"An unmapped label","???","https://checkyourfact.com/x","Check Your Fact","2020-01-01"
"Missing url row","TRUE","","NoURL","2020-01-01"
`

func TestRunSeedsWithProvenance(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Enabled: true, Vintage: "2023", MaxPriority: 9})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub, strings.NewReader(seedCSV))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 3 rows have url+claim; the missing-url row is skipped. Unverifiable counts only
	// the unmapped fallback ("???"); "MIXTURE" maps to unverifiable via the table.
	if stats.Published != 3 || stats.Skipped != 1 || stats.Unverifiable != 1 {
		t.Fatalf("stats = %+v, want Published=3 Skipped=1 Unverifiable=1", stats)
	}
	jobs := pub.jobs(t)
	byID := map[string]factcheckjob.ClaimJob{}
	for _, j := range jobs {
		byID[j.ID] = j
		if !strings.Contains(j.SourceName, "ClaimsKG") || !strings.Contains(j.SourceName, "2023") {
			t.Errorf("job missing ClaimsKG/vintage provenance: %q", j.SourceName)
		}
	}
	snopes := byID["https://www.snopes.com/fact-check/flat-earth"]
	if snopes.LiteralVerdict != string(domain.LiteralInaccurate) {
		t.Errorf("snopes verdict = %q, want inaccurate", snopes.LiteralVerdict)
	}
	if snopes.Outlet != "www.snopes.com" {
		t.Errorf("snopes outlet = %q, want www.snopes.com", snopes.Outlet)
	}
	afp := byID["https://factuel.afp.com/vaccins"]
	if afp.LiteralVerdict != string(domain.LiteralUnverifiable) {
		t.Errorf("afp MIXTURE verdict = %q, want unverifiable", afp.LiteralVerdict)
	}
}

func TestRunDisabledIsNoOp(t *testing.T) {
	t.Parallel()
	c, err := New(Config{Enabled: false, MaxPriority: 9})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub, strings.NewReader(seedCSV))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 0 || len(pub.bodies) != 0 {
		t.Fatalf("disabled seed published %d jobs, want 0", stats.Published)
	}
	if c.Enabled() {
		t.Error("Enabled() = true for a disabled seed")
	}
}

func TestRunMissingOptionalColumnsNeverReadClaimText(t *testing.T) {
	t.Parallel()
	// Only the two required columns are present; rating/author/date are absent. They
	// must NOT default to column 0 (the claim text) — the historical bug.
	csvData := "claimReviewed,claimReview_url\n" +
		"\"La Terre est plate\",\"https://www.snopes.com/x\"\n"
	c, err := New(Config{Enabled: true, MaxPriority: 9})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub, strings.NewReader(csvData))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 1 || stats.Unverifiable != 1 {
		t.Fatalf("stats = %+v, want Published=1 Unverifiable=1 (no rating column)", stats)
	}
	j := pub.jobs(t)[0]
	if j.LiteralVerdict != string(domain.LiteralUnverifiable) {
		t.Errorf("verdict = %q, want unverifiable (rating column absent)", j.LiteralVerdict)
	}
	if j.CheckedAt != "" {
		t.Errorf("checkedAt = %q, want empty (date column absent, not the claim text)", j.CheckedAt)
	}
	// source_name is the provenance string; it must never be the claim text read from
	// a mis-indexed author column.
	if strings.Contains(j.SourceName, "La Terre est plate") {
		t.Errorf("source name leaked claim text: %q", j.SourceName)
	}
}

func TestRunTSVDelimiterAndMissingColumns(t *testing.T) {
	t.Parallel()
	tsv := "claimReviewed\trating\tclaimReview_url\n" +
		"Une affirmation\tFaux\thttps://factuel.afp.com/x\n"
	c, _ := New(Config{Enabled: true, MaxPriority: 9, Delimiter: '\t'})
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub, strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 1 {
		t.Fatalf("published = %d, want 1", stats.Published)
	}

	// A header missing the required columns is a hard error.
	bad := "foo,bar\n1,2\n"
	c2, _ := New(Config{Enabled: true, MaxPriority: 9})
	if _, err := c2.Run(context.Background(), nil, &recordingPublisher{}, strings.NewReader(bad)); err == nil {
		t.Fatal("expected an error for an export missing claim/url columns")
	}
}
