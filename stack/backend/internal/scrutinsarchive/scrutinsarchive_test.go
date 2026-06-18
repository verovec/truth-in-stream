package scrutinsarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

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
