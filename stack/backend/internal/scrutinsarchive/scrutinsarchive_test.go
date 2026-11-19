package scrutinsarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsjob"
	"github.com/verovec/truth-in-stream/backend/internal/votingrecord"
)

// scrutinFile is one real-shaped inner archive file: the {"scrutin": {...}}
// envelope the AN open-data archive wraps each scrutin in, mirroring
// internal/votingrecord/testdata. Two voters keep the per-scrutin record count
// observable end to end.
func scrutinFile(uid string) []byte {
	return []byte(`{
  "scrutin": {
    "uid": "` + uid + `",
    "numero": "42",
    "legislature": "17",
    "dateScrutin": "2024-10-15",
    "objet": {"libelle": "sur l'ensemble du projet de loi de finances pour 2025"},
    "ventilationVotes": {"organe": {"groupes": {"groupe": [
      {"vote": {"decompteNominatif": {
        "pours": {"votant": [{"acteurRef": "PA1592"}]},
        "contres": {"votant": {"acteurRef": "PA721002"}}
      }}}
    ]}}}
  }
}`)
}

// buildArchive zips the given inner scrutin files under VTANR-style entry names,
// plus a non-JSON entry the discovery must ignore.
func buildArchive(t *testing.T, uids ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, uid := range uids {
		w, err := zw.Create(uid + ".json")
		if err != nil {
			t.Fatalf("zip create %s: %v", uid, err)
		}
		if _, err := w.Write(scrutinFile(uid)); err != nil {
			t.Fatalf("zip write %s: %v", uid, err)
		}
	}
	ignore, err := zw.Create("README.txt")
	if err != nil {
		t.Fatalf("zip create readme: %v", err)
	}
	if _, err := ignore.Write([]byte("not a scrutin")); err != nil {
		t.Fatalf("zip write readme: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// archiveServer serves the archive with an ETag, honoring If-None-Match with a
// 304 so the conditional-GET skip is exercised against a real HTTP round-trip. It
// counts how many times the body was actually sent.
type archiveServer struct {
	etag  string
	body  []byte
	mu    sync.Mutex
	count int
}

func (s *archiveServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", s.etag)
		if match := r.Header.Get("If-None-Match"); match == s.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		s.mu.Lock()
		s.count++
		body := s.body
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(body)
	}
}

func (s *archiveServer) bodyServed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// recordingPublisher captures every published job body.
type recordingPublisher struct {
	mu     sync.Mutex
	bodies [][]byte
	err    error
}

func (p *recordingPublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	if p.err != nil {
		return p.err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(body))
	copy(cp, body)
	p.bodies = append(p.bodies, cp)
	return nil
}

func (p *recordingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func newClient(t *testing.T, url, markerPath string, pub Publisher) *Client {
	t.Helper()
	c, err := New(Config{
		Legislature: "17",
		MarkerPath:  markerPath,
		MaxPriority: 10,
		// The %s slot consumes the legislature into a harmless query the test
		// server ignores, so the production Sprintf interpolation is exercised.
		URLTemplate: url + "?legislature=%s",
	}, pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRunDiscoversAndPublishes(t *testing.T) {
	t.Parallel()
	srv := &archiveServer{etag: `"v1"`, body: buildArchive(t, "VTANR5L17V42", "VTANR5L17V43")}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	pub := &recordingPublisher{}
	c := newClient(t, ts.URL, filepath.Join(t.TempDir(), "marker.json"), pub)

	stats, err := c.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 2 {
		t.Fatalf("published %d scrutins, want 2 (the README must be ignored)", stats.New)
	}
	if pub.count() != 2 {
		t.Fatalf("publisher saw %d jobs, want 2", pub.count())
	}

	// Each job carries the bare scrutin object; the worker's wrap + the existing
	// parser must turn it into the same records as the legacy -dir path.
	var job scrutinsjob.ScrutinJob
	if err := json.Unmarshal(pub.bodies[0], &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.ID == "" {
		t.Fatal("job carries no scrutin uid")
	}
	records, err := votingrecord.ParseScrutin(wrapForParser(job.Scrutin))
	if err != nil {
		t.Fatalf("parse published scrutin: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("parsed %d records from a published scrutin, want 2", len(records))
	}
}

// wrapForParser mirrors the worker's envelope restoration so the test asserts the
// published payload is parseable by the real parser.
func wrapForParser(inner json.RawMessage) []byte {
	return append(append([]byte(`{"scrutin":`), inner...), '}')
}

func TestRunSkipsUnchangedArchive(t *testing.T) {
	t.Parallel()
	srv := &archiveServer{etag: `"v1"`, body: buildArchive(t, "VTANR5L17V42")}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	markerPath := filepath.Join(t.TempDir(), "marker.json")

	first := newClient(t, ts.URL, markerPath, &recordingPublisher{})
	if _, err := first.Run(t.Context()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if srv.bodyServed() != 1 {
		t.Fatalf("body served %d times on first run, want 1", srv.bodyServed())
	}

	pub := &recordingPublisher{}
	second := newClient(t, ts.URL, markerPath, pub)
	stats, err := second.Run(t.Context())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if srv.bodyServed() != 1 {
		t.Fatalf("body served %d times after a 304, want still 1", srv.bodyServed())
	}
	if stats.New != 0 || pub.count() != 0 {
		t.Fatalf("unchanged archive published %d jobs (stats.New=%d), want 0", pub.count(), stats.New)
	}
}

func TestRunRepublishesWhenArchiveChanges(t *testing.T) {
	t.Parallel()
	srv := &archiveServer{etag: `"v1"`, body: buildArchive(t, "VTANR5L17V42")}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	markerPath := filepath.Join(t.TempDir(), "marker.json")
	if _, err := newClient(t, ts.URL, markerPath, &recordingPublisher{}).Run(t.Context()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// A new archive version invalidates the stored ETag, so the next run is a 200
	// and republishes.
	srv.etag = `"v2"`
	srv.body = buildArchive(t, "VTANR5L17V42", "VTANR5L17V44")
	pub := &recordingPublisher{}
	stats, err := newClient(t, ts.URL, markerPath, pub).Run(t.Context())
	if err != nil {
		t.Fatalf("changed Run: %v", err)
	}
	if stats.New != 2 || pub.count() != 2 {
		t.Fatalf("changed archive published %d jobs (stats.New=%d), want 2", pub.count(), stats.New)
	}
}

func TestRunWithoutMarkerAlwaysDownloads(t *testing.T) {
	t.Parallel()
	srv := &archiveServer{etag: `"v1"`, body: buildArchive(t, "VTANR5L17V42")}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	// Empty marker path disables the skip: every run downloads and publishes.
	for i := range 2 {
		pub := &recordingPublisher{}
		stats, err := newClient(t, ts.URL, "", pub).Run(t.Context())
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if stats.New != 1 {
			t.Fatalf("run %d published %d, want 1", i, stats.New)
		}
	}
	if srv.bodyServed() != 2 {
		t.Fatalf("body served %d times without a marker, want 2", srv.bodyServed())
	}
}

// recordingNotifier captures the crawl events for the alert-lifecycle assertion.
type recordingNotifier struct{ events []crawlnotify.CrawlEvent }

func (r *recordingNotifier) Notify(_ context.Context, e crawlnotify.CrawlEvent) error {
	r.events = append(r.events, e)
	return nil
}

func TestRunWithAlertsLifecycle(t *testing.T) {
	t.Parallel()
	srv := &archiveServer{etag: `"v1"`, body: buildArchive(t, "VTANR5L17V42")}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c := newClient(t, ts.URL, filepath.Join(t.TempDir(), "marker.json"), &recordingPublisher{})
	rec := &recordingNotifier{}

	stats, err := crawlnotify.RunWithAlerts(t.Context(), rec, c)
	if err != nil {
		t.Fatalf("RunWithAlerts: %v", err)
	}
	if stats.New != 1 {
		t.Fatalf("stats.New = %d, want 1", stats.New)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want 2 (started, finished)", len(rec.events))
	}
	if _, ok := rec.events[0].(crawlnotify.RunStarted); !ok {
		t.Fatalf("first event = %T, want RunStarted", rec.events[0])
	}
	fin, ok := rec.events[1].(crawlnotify.RunFinished)
	if !ok {
		t.Fatalf("second event = %T, want RunFinished", rec.events[1])
	}
	if fin.Source != "scrutins" || fin.Scope != "legislature:17" || fin.New != 1 {
		t.Fatalf("finish event = %+v, want scrutins/legislature:17/New=1", fin)
	}
}

func TestRunWithoutServerValidatorsRedownloads(t *testing.T) {
	t.Parallel()
	// A server that serves neither ETag nor Last-Modified: the unchanged-archive
	// skip cannot engage, so every run downloads and republishes (idempotently)
	// rather than persisting an empty marker that could never yield a 304.
	served := 0
	body := buildArchive(t, "VTANR5L17V42")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	markerPath := filepath.Join(t.TempDir(), "marker.json")
	for i := range 2 {
		pub := &recordingPublisher{}
		stats, err := newClient(t, ts.URL, markerPath, pub).Run(t.Context())
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if stats.New != 1 || pub.count() != 1 {
			t.Fatalf("run %d published %d (stats.New=%d), want 1", i, pub.count(), stats.New)
		}
	}
	if served != 2 {
		t.Fatalf("body served %d times without validators, want 2 (skip must stay disabled)", served)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("marker file exists with no validators (err=%v), want absent", err)
	}
}

func TestRunFailsOnUnexpectedStatus(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer ts.Close()

	c := newClient(t, ts.URL, filepath.Join(t.TempDir(), "marker.json"), &recordingPublisher{})
	if _, err := c.Run(t.Context()); err == nil {
		t.Fatal("Run over a 410 returned nil error, want error")
	}
}

// scriptedTransport answers each request from a fixed script with no network, so
// the retry waits run on synctest's fake clock. A request past the end of the
// script gets the last response again.
type scriptedTransport struct {
	mu        sync.Mutex
	script    []scriptedResponse
	userAgent []string
}

type scriptedResponse struct {
	status     int
	retryAfter string
	body       []byte
}

func (s *scriptedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	next := s.script[min(len(s.userAgent), len(s.script)-1)]
	s.userAgent = append(s.userAgent, r.Header.Get("User-Agent"))
	s.mu.Unlock()

	header := http.Header{}
	if next.retryAfter != "" {
		header.Set("Retry-After", next.retryAfter)
	}
	return &http.Response{
		StatusCode: next.status,
		Status:     fmt.Sprintf("%d %s", next.status, http.StatusText(next.status)),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(next.body)),
		Request:    r,
	}, nil
}

func (s *scriptedTransport) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.userAgent)
}

func newScriptedClient(t *testing.T, transport *scriptedTransport, pub Publisher) *Client {
	t.Helper()
	c, err := New(Config{
		Legislature: "17",
		MaxPriority: 10,
		URLTemplate: "http://archive.test/%s/Scrutins.json.zip",
		HTTPClient:  &http.Client{Transport: transport},
	}, pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRunSendsDescriptiveUserAgent(t *testing.T) {
	t.Parallel()
	transport := &scriptedTransport{script: []scriptedResponse{
		{status: http.StatusOK, body: buildArchive(t, "VTANR5L17V1")},
	}}
	c := newScriptedClient(t, transport, &recordingPublisher{})
	if _, err := c.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := transport.userAgent[0]; got != userAgent {
		t.Errorf("User-Agent = %q, want %q", got, userAgent)
	}
}

func TestRunRetriesThrottledDownload(t *testing.T) {
	t.Parallel()
	throttled := func(status int, retryAfter string) scriptedResponse {
		return scriptedResponse{status: status, retryAfter: retryAfter}
	}
	tests := []struct {
		name         string
		throttles    []scriptedResponse
		wantWait     time.Duration
		wantRequests int
	}{
		{
			name:         "429 with Retry-After seconds waits exactly that long",
			throttles:    []scriptedResponse{throttled(http.StatusTooManyRequests, "7")},
			wantWait:     7 * time.Second,
			wantRequests: 2,
		},
		{
			name: "throttles without Retry-After back off exponentially",
			throttles: []scriptedResponse{
				throttled(http.StatusServiceUnavailable, ""),
				throttled(http.StatusTooManyRequests, ""),
			},
			wantWait:     downloadRetryBase + 2*downloadRetryBase,
			wantRequests: 3,
		},
		{
			name: "succeeds on the last allowed retry",
			throttles: []scriptedResponse{
				throttled(http.StatusTooManyRequests, "1"),
				throttled(http.StatusTooManyRequests, "1"),
				throttled(http.StatusTooManyRequests, "1"),
				throttled(http.StatusTooManyRequests, "1"),
			},
			wantWait:     4 * time.Second,
			wantRequests: 5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			archive := buildArchive(t, "VTANR5L17V1", "VTANR5L17V2")
			synctest.Test(t, func(t *testing.T) {
				script := append(slices.Clone(tc.throttles), scriptedResponse{status: http.StatusOK, body: archive})
				transport := &scriptedTransport{script: script}
				pub := &recordingPublisher{}
				c := newScriptedClient(t, transport, pub)

				start := time.Now()
				stats, err := c.Run(t.Context())
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if got := time.Since(start); got != tc.wantWait {
					t.Errorf("waited %s, want %s", got, tc.wantWait)
				}
				if got := transport.requests(); got != tc.wantRequests {
					t.Errorf("requests = %d, want %d", got, tc.wantRequests)
				}
				if stats.New != 2 || pub.count() != 2 {
					t.Errorf("published stats.New=%d jobs=%d, want 2 and 2", stats.New, pub.count())
				}
			})
		})
	}
}

func TestRunFailsWhenThrottlingPersists(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		retryAfter   string
		wantWait     time.Duration
		wantRequests int
		wantInError  string
	}{
		{
			name:         "gives up after the retry budget",
			retryAfter:   "",
			wantWait:     (1 + 2 + 4 + 8) * downloadRetryBase,
			wantRequests: downloadMaxRetries + 1,
			wantInError:  "gave up after 4 retries",
		},
		{
			name:         "refuses a Retry-After beyond the wait limit without retrying",
			retryAfter:   "3600",
			wantWait:     0,
			wantRequests: 1,
			wantInError:  "host asked to wait 1h0m0s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				transport := &scriptedTransport{script: []scriptedResponse{
					{status: http.StatusTooManyRequests, retryAfter: tc.retryAfter},
				}}
				pub := &recordingPublisher{}
				c := newScriptedClient(t, transport, pub)

				start := time.Now()
				_, err := c.Run(t.Context())
				if err == nil {
					t.Fatal("Run over a persistent 429 returned nil error, want error")
				}
				for _, want := range []string{tc.wantInError, "429 Too Many Requests"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err, want)
					}
				}
				if got := time.Since(start); got != tc.wantWait {
					t.Errorf("waited %s, want %s", got, tc.wantWait)
				}
				if got := transport.requests(); got != tc.wantRequests {
					t.Errorf("requests = %d, want %d", got, tc.wantRequests)
				}
				if pub.count() != 0 {
					t.Errorf("published %d jobs from a failed download, want 0", pub.count())
				}
			})
		})
	}
}

func TestRunStopsWaitingWhenCanceled(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		transport := &scriptedTransport{script: []scriptedResponse{
			{status: http.StatusTooManyRequests, retryAfter: "60"},
		}}
		c := newScriptedClient(t, transport, &recordingPublisher{})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(10*time.Second, cancel)

		start := time.Now()
		_, err := c.Run(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
		if got := time.Since(start); got != 10*time.Second {
			t.Errorf("returned after %s, want 10s (the cancellation, not the 60s Retry-After)", got)
		}
		if got := transport.requests(); got != 1 {
			t.Errorf("requests = %d, want 1", got)
		}
	})
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "empty", header: "", want: 0},
		{name: "whole seconds", header: "120", want: 2 * time.Minute},
		{name: "padded seconds", header: " 5 ", want: 5 * time.Second},
		{name: "zero seconds", header: "0", want: 0},
		{name: "negative seconds", header: "-3", want: 0},
		{name: "future HTTP-date", header: now.Add(90 * time.Second).Format(http.TimeFormat), want: 90 * time.Second},
		{name: "past HTTP-date", header: now.Add(-time.Hour).Format(http.TimeFormat), want: 0},
		{name: "garbage", header: "soon", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRetryAfter(tc.header, now); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.header, got, tc.want)
			}
		})
	}
}
