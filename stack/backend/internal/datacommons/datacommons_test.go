package datacommons

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// recordingPublisher captures every published body and priority.
type recordingPublisher struct {
	mu     sync.Mutex
	bodies [][]byte
	prio   []uint8
	err    error
}

func (p *recordingPublisher) Publish(_ context.Context, body []byte, priority uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.bodies = append(p.bodies, body)
	p.prio = append(p.prio, priority)
	return nil
}

func (p *recordingPublisher) jobs(t *testing.T) []factcheckjob.ClaimJob {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	jobs := make([]factcheckjob.ClaimJob, 0, len(p.bodies))
	for _, b := range p.bodies {
		var j factcheckjob.ClaimJob
		if err := json.Unmarshal(b, &j); err != nil {
			t.Fatalf("decode published body: %v", err)
		}
		jobs = append(jobs, j)
	}
	return jobs
}

// fixtureServer serves the committed real-wire-format DataFeed fixture.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile("testdata/feed.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
}

func newTestClient(t *testing.T, srv *httptest.Server, cfg Config) *Client {
	t.Helper()
	cfg.FeedURL = srv.URL
	cfg.MaxPriority = 9
	cfg.HTTPClient = srv.Client()
	cfg.Retry = httpx.RetryConfig{MaxRetries: -1}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// defaultFRAllowlist mirrors the config default so the test exercises the same
// French outlet allowlist the producer ships with.
var defaultFRAllowlist = []string{
	"factuel.afp.com", "lemonde.fr", "francetvinfo.fr",
	"20minutes.fr", "liberation.fr", "observers.france24.com",
}

func TestRunAllowlistedFrenchOutlets(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()
	c := newTestClient(t, srv, Config{OutletAllowlist: defaultFRAllowlist})

	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// afp (inaccurate), lemonde (inaccurate), francetvinfo (accurate via numeric),
	// 20minutes (unverifiable). politifact is off-allowlist; the empty-claim afp
	// record has no claim text: both skipped.
	if stats.Published != 4 || stats.Skipped != 2 || stats.Unverifiable != 1 {
		t.Fatalf("stats = %+v, want Published=4 Skipped=2 Unverifiable=1", stats)
	}

	byID := map[string]factcheckjob.ClaimJob{}
	for _, j := range pub.jobs(t) {
		byID[j.ID] = j
	}
	afp := byID["https://factuel.afp.com/un-dirigeable-amazon-non-une-realisation-3d"]
	if afp.LiteralVerdict != string(domain.LiteralInaccurate) {
		t.Errorf("afp verdict = %q, want inaccurate", afp.LiteralVerdict)
	}
	if afp.Outlet != "factuel.afp.com" {
		t.Errorf("afp outlet = %q, want factuel.afp.com", afp.Outlet)
	}
	if afp.SourceURL != afp.ID || afp.Text == "" || afp.QuotedSpan != afp.Text {
		t.Errorf("afp job not self-contained: %+v", afp)
	}
	if afp.CheckedAt != "2019-04-04T00:00:00Z" {
		t.Errorf("afp checkedAt = %q, want 2019-04-04T00:00:00Z", afp.CheckedAt)
	}

	fi := byID["https://www.francetvinfo.fr/vrai-ou-faux/citation-ministre_6100001.html"]
	if fi.LiteralVerdict != string(domain.LiteralAccurate) {
		t.Errorf("francetvinfo verdict = %q, want accurate (numeric 5/5)", fi.LiteralVerdict)
	}
	tm := byID["https://www.20minutes.fr/fake-off/migrations_5000123.html"]
	if tm.LiteralVerdict != string(domain.LiteralUnverifiable) {
		t.Errorf("20minutes verdict = %q, want unverifiable (unmapped rating)", tm.LiteralVerdict)
	}
	lm := byID["https://www.lemonde.fr/les-decodeurs/article/2019/11/21/mode-de-scrutin_6020046_4355770.html"]
	if lm.CheckedAt != "2019-11-21T00:00:00Z" {
		t.Errorf("lemonde checkedAt = %q, want 2019-11-21T00:00:00Z", lm.CheckedAt)
	}
	for _, p := range pub.prio {
		if p != 9 {
			t.Errorf("priority = %d, want ceiling 9", p)
		}
	}
}

func TestRunEmptyAllowlistIngestsEveryOutlet(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()
	c := newTestClient(t, srv, Config{OutletAllowlist: nil})

	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Only the empty-claim record is skipped now; politifact is admitted.
	if stats.Published != 5 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, want Published=5 Skipped=1", stats)
	}
	var pf *factcheckjob.ClaimJob
	for _, j := range pub.jobs(t) {
		if j.Outlet == "www.politifact.com" {
			jj := j
			pf = &jj
		}
	}
	if pf == nil {
		t.Fatal("politifact record not ingested with empty allowlist")
	}
	// "Mostly False" maps through the shared rating table (which covers the common
	// English fact-check labels alongside the French ones).
	if pf.LiteralVerdict != string(domain.LiteralInaccurate) {
		t.Errorf("politifact verdict = %q, want inaccurate", pf.LiteralVerdict)
	}
}

func TestRunMaxItemsCap(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()
	c := newTestClient(t, srv, Config{OutletAllowlist: defaultFRAllowlist, MaxItems: 2})

	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The cap counts examined records (published+skipped): the first two feed
	// records are both allowlisted, so exactly two publish before the cap stops
	// the walk.
	if stats.Published != 2 || stats.Skipped != 0 {
		t.Fatalf("stats = %+v, want Published=2 Skipped=0 under MaxItems=2", stats)
	}
}

func TestRunPublishErrorFailsRun(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()
	c := newTestClient(t, srv, Config{OutletAllowlist: defaultFRAllowlist})

	pub := &recordingPublisher{err: assertErr}
	if _, err := c.Run(context.Background(), nil, pub); err == nil {
		t.Fatal("Run returned nil on publish error, want error")
	}
}

func TestRunNon2xxFails(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, Config{OutletAllowlist: defaultFRAllowlist})
	if _, err := c.Run(context.Background(), nil, &recordingPublisher{}); err == nil {
		t.Fatal("Run returned nil on 500, want error")
	}
}

func TestNewRejectsZeroPriority(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{MaxPriority: 0}); err == nil {
		t.Fatal("New accepted MaxPriority=0, want error")
	}
}

var assertErr = &publishFailure{}

type publishFailure struct{}

func (*publishFailure) Error() string { return "publish failed" }
