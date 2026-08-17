package factcheckarchive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

func TestMapVerdict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rating string
		want   domain.LiteralVerdict
		ok     bool
	}{
		{"Faux", domain.LiteralInaccurate, true},
		{"FAUX", domain.LiteralInaccurate, true},
		{"Fausse", domain.LiteralInaccurate, true},
		{"Plutôt faux", domain.LiteralInaccurate, true},
		{"Trompeur", domain.LiteralInaccurate, true},
		{"Vrai", domain.LiteralAccurate, true},
		{"VRAI", domain.LiteralAccurate, true},
		{"Plutôt vrai", domain.LiteralAccurate, true},
		{"Exact", domain.LiteralAccurate, true},
		// Negated forms must not be swallowed by the "correct"/"exact" substrings.
		{"Incorrect", domain.LiteralInaccurate, true},
		{"Inexact", domain.LiteralInaccurate, true},
		{"Pas de preuve", domain.LiteralUnverifiable, true},
		{"On n'a pas pu vérifier", domain.LiteralUnverifiable, true},
		{"Invérifiable", domain.LiteralUnverifiable, true},
		{"  faux  ", domain.LiteralInaccurate, true},
		{"", "", false},
		{"gibberish-rating", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.rating, func(t *testing.T) {
			t.Parallel()
			got, ok := mapVerdict(tc.rating)
			if ok != tc.ok {
				t.Fatalf("mapVerdict(%q) ok = %v, want %v", tc.rating, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("mapVerdict(%q) = %q, want %q", tc.rating, got, tc.want)
			}
		})
	}
}

// recordingPublisher captures every published body and priority.
type recordingPublisher struct {
	mu     sync.Mutex
	bodies [][]byte
	prio   []uint8
}

func (p *recordingPublisher) Publish(_ context.Context, body []byte, priority uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
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

// fixtureServer serves claims_page1.json then claims_page2.json keyed on the
// pageToken query param, exactly as the Google Fact Check Tools API paginates.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	page1, err := os.ReadFile("testdata/claims_page1.json")
	if err != nil {
		t.Fatalf("read page1: %v", err)
	}
	page2, err := os.ReadFile("testdata/claims_page2.json")
	if err != nil {
		t.Fatalf("read page2: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "CONTINUE_TOKEN_PAGE2" {
			_, _ = w.Write(page2)
			return
		}
		_, _ = w.Write(page1)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	// Retries disabled so the HTTP-failure test asserts the first outcome without
	// backoff waits; retry behavior is covered by TestRunRetriesOnThrottle and the
	// httpx helper's own tests.
	c, err := New(Config{BaseURL: baseURL, APIKey: "test-key", LanguageCode: "fr", MaxPriority: 10, Retry: httpx.RetryConfig{MaxRetries: -1}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRunFollowsPaginationAndPublishesSelfContainedJobs(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	pub := &recordingPublisher{}
	client := newTestClient(t, srv.URL)

	stats, err := client.Run(t.Context(), nil, pub, RunConfig{Query: "élection"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Page1 has 2 claims, page2 has 2 claims; all four carry a mappable verdict,
	// so following nextPageToken yields four published jobs (pagination honored).
	if stats.Published != 4 {
		t.Fatalf("published = %d, want 4 (must follow nextPageToken)", stats.Published)
	}
	if stats.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", stats.Skipped)
	}

	jobs := pub.jobs(t)
	if len(jobs) != 4 {
		t.Fatalf("published %d jobs, want 4", len(jobs))
	}

	byID := map[string]factcheckjob.ClaimJob{}
	for _, j := range jobs {
		byID[j.ID] = j
	}

	afp := byID["https://factuel.afp.com/doc.afp.com.34PQ6WA"]
	if afp.Text != "La criminalité a augmenté de 50% en France depuis 2017." {
		t.Errorf("afp claim text = %q", afp.Text)
	}
	if afp.LiteralVerdict != string(domain.LiteralInaccurate) {
		t.Errorf("afp verdict = %q, want inaccurate", afp.LiteralVerdict)
	}
	if afp.SourceName != "AFP Factuel" || afp.Outlet != "factuel.afp.com" {
		t.Errorf("afp source/outlet = %q/%q", afp.SourceName, afp.Outlet)
	}
	if afp.SourceURL != "https://factuel.afp.com/doc.afp.com.34PQ6WA" {
		t.Errorf("afp source url = %q", afp.SourceURL)
	}
	if afp.QuotedSpan != "La criminalité a augmenté de 50% en France depuis 2017." {
		t.Errorf("afp quoted span = %q", afp.QuotedSpan)
	}
	if afp.CheckedAt != "2024-03-18T00:00:00Z" {
		t.Errorf("afp checked-at = %q", afp.CheckedAt)
	}

	lemonde := byID["https://www.lemonde.fr/les-decodeurs/article/2024/03/22/intox_6543211_4355770.html"]
	if lemonde.LiteralVerdict != string(domain.LiteralInaccurate) { // "Trompeur"
		t.Errorf("lemonde verdict = %q, want inaccurate", lemonde.LiteralVerdict)
	}
	if lemonde.Outlet != "lemonde.fr" {
		t.Errorf("lemonde outlet = %q", lemonde.Outlet)
	}

	// Every published job must validate as a worker would see it (self-contained).
	for _, j := range jobs {
		if j.ID == "" || j.SourceURL == "" || j.Outlet == "" {
			t.Errorf("job %+v is not self-contained", j)
		}
	}
}

func TestRunIsIdempotentAcrossRuns(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	client := newTestClient(t, srv.URL)

	first := &recordingPublisher{}
	if _, err := client.Run(t.Context(), nil, first, RunConfig{Query: "x"}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second := &recordingPublisher{}
	if _, err := client.Run(t.Context(), nil, second, RunConfig{Query: "x"}); err != nil {
		t.Fatalf("second run: %v", err)
	}

	firstByID := map[string]string{}
	for _, j := range first.jobs(t) {
		b, _ := json.Marshal(j)
		firstByID[j.ID] = string(b)
	}
	secondByID := map[string]string{}
	for _, j := range second.jobs(t) {
		b, _ := json.Marshal(j)
		secondByID[j.ID] = string(b)
	}
	if len(firstByID) != len(secondByID) {
		t.Fatalf("run produced %d vs %d distinct ids", len(firstByID), len(secondByID))
	}
	for id, body := range firstByID {
		if secondByID[id] != body {
			t.Fatalf("job %q differs across runs:\n  %s\n  %s", id, body, secondByID[id])
		}
	}
}

// noVerdictServer returns a claim whose textualRating cannot be mapped, so the
// producer must skip it (count it) rather than publish a job with an empty verdict.
func TestRunSkipsUnmappableVerdicts(t *testing.T) {
	t.Parallel()
	body := `{"claims":[{"text":"Une affirmation.","claimReview":[{"publisher":{"name":"AFP Factuel","site":"factuel.afp.com"},"url":"https://factuel.afp.com/doc.afp.com.AAAA","title":"t","reviewDate":"2024-01-01T00:00:00Z","textualRating":"???","languageCode":"fr"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	pub := &recordingPublisher{}
	client := newTestClient(t, srv.URL)
	stats, err := client.Run(t.Context(), nil, pub, RunConfig{Query: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 0 || stats.Skipped != 1 {
		t.Fatalf("published=%d skipped=%d, want 0/1", stats.Published, stats.Skipped)
	}
	if len(pub.bodies) != 0 {
		t.Fatalf("unmappable verdict must not publish")
	}
}

func TestRunSkipsClaimWithNoReview(t *testing.T) {
	t.Parallel()
	body := `{"claims":[{"text":"Une affirmation sans review."}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	pub := &recordingPublisher{}
	client := newTestClient(t, srv.URL)
	stats, err := client.Run(t.Context(), nil, pub, RunConfig{Query: "x"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 0 {
		t.Fatalf("published = %d, want 0", stats.Published)
	}
}

func TestRunSendsAPIKeyAndLanguage(t *testing.T) {
	t.Parallel()
	var gotKey, gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotLang = r.URL.Query().Get("languageCode")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"claims":[]}`))
	}))
	t.Cleanup(srv.Close)

	client := newTestClient(t, srv.URL)
	if _, err := client.Run(t.Context(), nil, &recordingPublisher{}, RunConfig{Query: "x"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotKey != "test-key" {
		t.Errorf("key = %q, want test-key", gotKey)
	}
	if gotLang != "fr" {
		t.Errorf("languageCode = %q, want fr", gotLang)
	}
}

// TestRunRetriesOnThrottle proves the fetcher backs off and retries a 429 from the
// Fact Check Tools API instead of failing the run: the first request is throttled,
// the retry returns an empty page, and Run completes. A tiny base delay keeps it fast.
func TestRunRetriesOnThrottle(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"claims":[]}`))
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{BaseURL: srv.URL, APIKey: "test-key", LanguageCode: "fr", MaxPriority: 10, Retry: httpx.RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Run(t.Context(), nil, &recordingPublisher{}, RunConfig{Query: "x"}); err != nil {
		t.Fatalf("Run after a throttled first attempt: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("server hit %d times, want 2 (one throttle + one retry)", calls.Load())
	}
}

func TestRunReturnsErrorOnHTTPFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	client := newTestClient(t, srv.URL)
	if _, err := client.Run(t.Context(), nil, &recordingPublisher{}, RunConfig{Query: "x"}); err == nil {
		t.Fatal("expected an error on HTTP 429")
	}
}

func TestRunFallsBackToSiteWhenPublisherNameMissing(t *testing.T) {
	t.Parallel()
	body := `{"claims":[{"text":"Une affirmation.","claimReview":[{"publisher":{"site":"factuel.afp.com"},"url":"https://factuel.afp.com/doc.afp.com.BBBB","textualRating":"Faux","languageCode":"fr"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	pub := &recordingPublisher{}
	client := newTestClient(t, srv.URL)
	if _, err := client.Run(t.Context(), nil, pub, RunConfig{Query: "x"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	jobs := pub.jobs(t)
	if len(jobs) != 1 {
		t.Fatalf("published %d jobs, want 1", len(jobs))
	}
	// source_name is NOT NULL in the store: a publisher with only a site must
	// still yield a non-empty source name (the site host).
	if jobs[0].SourceName != "factuel.afp.com" {
		t.Errorf("source name = %q, want the site fallback", jobs[0].SourceName)
	}
	if jobs[0].Outlet != "factuel.afp.com" {
		t.Errorf("outlet = %q", jobs[0].Outlet)
	}
}

func TestRunErrorDoesNotLeakAPIKey(t *testing.T) {
	t.Parallel()
	// A server that hijacks and drops the connection forces a transport error
	// (the *url.Error path), which carries the request URL - and the key - unless
	// redacted.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{BaseURL: srv.URL, APIKey: "super-secret-key", LanguageCode: "fr", MaxPriority: 10})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Run(t.Context(), nil, &recordingPublisher{}, RunConfig{Query: "x"})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("error message leaks the API key: %v", err)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{APIKey: "", MaxPriority: 10}); err == nil {
		t.Error("empty api key should fail")
	}
	if _, err := New(Config{APIKey: "k", MaxPriority: 0}); err == nil {
		t.Error("zero max priority should fail")
	}
}
