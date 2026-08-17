package parliament

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
)

// fakePub records every published job body.
type fakePub struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (p *fakePub) Publish(_ context.Context, body []byte, _ uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(body))
	copy(cp, body)
	p.bodies = append(p.bodies, cp)
	return nil
}

func (p *fakePub) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.bodies))
	for _, b := range p.bodies {
		var j connector.EvidenceJob
		if err := json.Unmarshal(b, &j); err == nil {
			out = append(out, j.ExternalID)
		}
	}
	return out
}

func (p *fakePub) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies = nil
}

// makeAmendement builds a wrapped amendement entry with a given uid and fate.
func makeAmendement(uid, sort string) []byte {
	return []byte(fmt.Sprintf(`{"amendement":{"uid":%q,"legislature":"17","identification":{"numeroLong":%q},"cycleDeVie":{"sort":%q},"corps":{"contenuAuteur":{"dispositif":"<p>texte</p>"}}}}`, uid, uid, sort))
}

// buildZip packs each entry as its own .json file in a zip archive.
func buildZip(t *testing.T, entries ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, e := range entries {
		w, err := zw.Create(fmt.Sprintf("amendement-%d.json", i))
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(e); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// dumpServer serves the current zip with a conditional GET: a request whose
// If-None-Match matches the current etag gets 304, else 200 with the zip. The test
// swaps zip/etag to simulate a refreshed dump.
type dumpServer struct {
	mu   sync.Mutex
	zip  []byte
	etag string
}

func (s *dumpServer) set(zip []byte, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zip, s.etag = zip, etag
}

func (s *dumpServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		zipData, etag := s.zip, s.etag
		s.mu.Unlock()
		if r.Header.Get("If-None-Match") == etag && etag != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipData)
	}
}

func newTestProducer(t *testing.T, url string, pub Publisher, dir string, maxItems int) *Producer {
	t.Helper()
	p, err := New(Config{
		Dataset:      DatasetANAmendements,
		Legislature:  "17",
		MarkerPath:   filepath.Join(dir, "marker.json"),
		ManifestPath: filepath.Join(dir, "manifest.json"),
		MaxPriority:  10,
		MaxItems:     maxItems,
		URLTemplate:  url + "/%s/Amendements.json.zip",
	}, pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestProducerPublishesThenSkipsUnchangedDump(t *testing.T) {
	t.Parallel()
	srv := &dumpServer{}
	srv.set(buildZip(t, makeAmendement("U1", "Adopté"), makeAmendement("U2", "Rejeté")), "v1")
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	dir := t.TempDir()
	pub := &fakePub{}
	p := newTestProducer(t, ts.URL, pub, dir, 0)

	// First run: fresh dump, both records new.
	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if stats.New != 2 || stats.Skipped != 0 {
		t.Fatalf("first run stats = %+v, want New=2 Skipped=0", stats)
	}
	if got := len(pub.ids()); got != 2 {
		t.Fatalf("first run published %d jobs, want 2", got)
	}

	// Second run: dump unchanged (304 via replayed ETag), nothing published.
	pub.reset()
	stats, err = p.Run(t.Context())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if stats.New != 0 || stats.Skipped != 0 {
		t.Fatalf("second run stats = %+v, want an all-zero skipped run (304)", stats)
	}
	if got := len(pub.ids()); got != 0 {
		t.Fatalf("second run published %d jobs, want 0 (unchanged dump)", got)
	}
}

func TestProducerManifestDiffRepublishesOnlyChangedRecords(t *testing.T) {
	t.Parallel()
	srv := &dumpServer{}
	srv.set(buildZip(t, makeAmendement("U1", "Adopté"), makeAmendement("U2", "Rejeté")), "v1")
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	dir := t.TempDir()
	pub := &fakePub{}
	p := newTestProducer(t, ts.URL, pub, dir, 0)

	if _, err := p.Run(t.Context()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// The dump changes: U1's fate flips, U2 is unchanged. A new ETag forces a 200,
	// and the manifest diff must republish only U1.
	pub.reset()
	srv.set(buildZip(t, makeAmendement("U1", "Rejeté"), makeAmendement("U2", "Rejeté")), "v2")

	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if stats.New != 1 || stats.Skipped != 1 {
		t.Fatalf("changed-dump stats = %+v, want New=1 Skipped=1", stats)
	}
	ids := pub.ids()
	if len(ids) != 1 || ids[0] != "U1" {
		t.Fatalf("republished ids = %v, want only [U1]", ids)
	}
}

func TestProducerMaxItemsBoundsARun(t *testing.T) {
	t.Parallel()
	srv := &dumpServer{}
	srv.set(buildZip(t, makeAmendement("U1", "Adopté"), makeAmendement("U2", "Rejeté"), makeAmendement("U3", "Tombé")), "v1")
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	dir := t.TempDir()
	pub := &fakePub{}
	p := newTestProducer(t, ts.URL, pub, dir, 1)

	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 1 {
		t.Fatalf("MaxItems=1 published %d, want 1", stats.New)
	}
	if got := len(pub.ids()); got != 1 {
		t.Fatalf("published %d jobs under MaxItems=1, want 1", got)
	}
}

func TestProducerMaxItemsIsResumableAcrossRuns(t *testing.T) {
	t.Parallel()
	srv := &dumpServer{}
	srv.set(buildZip(t, makeAmendement("U1", "Adopté"), makeAmendement("U2", "Rejeté")), "v1")
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	dir := t.TempDir()
	pub := &fakePub{}
	// MaxItems=1 publishes only U1 the first run; the deferred U2 must not be
	// fingerprinted, so a second run (new ETag to force a 200) publishes U2.
	p := newTestProducer(t, ts.URL, pub, dir, 1)

	if stats, err := p.Run(t.Context()); err != nil || stats.New != 1 {
		t.Fatalf("first run stats=%+v err=%v, want New=1", stats, err)
	}

	pub.reset()
	srv.set(buildZip(t, makeAmendement("U1", "Adopté"), makeAmendement("U2", "Rejeté")), "v2")
	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if stats.New != 1 {
		t.Fatalf("second run published %d, want the deferred remainder (1)", stats.New)
	}
	if ids := pub.ids(); len(ids) != 1 || ids[0] != "U2" {
		t.Fatalf("second run ids = %v, want only the deferred [U2]", ids)
	}
}

func TestNewRejectsUnknownDataset(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{Dataset: "senat-amendements", Legislature: "17", MaxPriority: 1}, &fakePub{}, nil); err == nil {
		t.Error("New must reject an unknown dataset")
	}
}
