package evidencesrc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestMarkerRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "marker.json")
	if m, err := LoadMarker(path); err != nil || !m.Empty() {
		t.Fatalf("missing marker: got %+v err %v, want empty", m, err)
	}
	want := Marker{ETag: "abc", LastModified: "Mon, 01 Jan 2026 00:00:00 GMT"}
	if err := SaveMarker(path, want); err != nil {
		t.Fatalf("SaveMarker: %v", err)
	}
	got, err := LoadMarker(path)
	if err != nil {
		t.Fatalf("LoadMarker: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestManifestDiff(t *testing.T) {
	t.Parallel()
	m := NewManifest()
	if !m.Changed("a", "fp1") {
		t.Error("unseen id must read as changed")
	}
	m.Set("a", "fp1")
	if m.Changed("a", "fp1") {
		t.Error("recorded id with same fingerprint must read as unchanged")
	}
	if !m.Changed("a", "fp2") {
		t.Error("recorded id with new fingerprint must read as changed")
	}
}

func TestManifestPersistence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "manifest.json")
	m := NewManifest()
	m.Set("id-1", "fp-1")
	m.Set("id-2", "fp-2")
	if err := SaveManifest(path, m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	back, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if back.Len() != 2 || back.Changed("id-1", "fp-1") {
		t.Errorf("manifest did not persist: len=%d", back.Len())
	}
}

func TestSplitChunksBreaksAtWhitespace(t *testing.T) {
	t.Parallel()
	if got := SplitChunks("short text", 100); len(got) != 1 {
		t.Fatalf("short text split into %d chunks, want 1", len(got))
	}
	long := strings.Repeat("mot ", 500)
	chunks := SplitChunks(long, 50)
	if len(chunks) < 2 {
		t.Fatalf("long text split into %d chunks, want >=2", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c)) > 50 {
			t.Errorf("chunk exceeds max runes: %d", len([]rune(c)))
		}
	}
}

func TestPlainTextStripsHTML(t *testing.T) {
	t.Parallel()
	if got := PlainText("<p>bonjour&nbsp;&amp; bienvenue</p>"); got != "bonjour & bienvenue" {
		t.Errorf("PlainText = %q", got)
	}
}

func TestBuildRecordKeysAndKinds(t *testing.T) {
	t.Parallel()
	rec := BuildRecord("src", "ext-1", "Title", "https://x", "lead body text", nil, map[string]any{"k": "v"})
	if rec.ExternalID != "ext-1" || rec.Fingerprint == "" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Jobs[0].Kind != string(domain.EvidenceKindLead) {
		t.Errorf("first chunk kind = %q, want lead", rec.Jobs[0].Kind)
	}
	if err := rec.Jobs[0].Validate(); err != nil {
		t.Errorf("job does not validate: %v", err)
	}
}

// capturePublisher records published bodies.
type capturePublisher struct {
	mu   sync.Mutex
	jobs []connector.EvidenceJob
}

func (c *capturePublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	var j connector.EvidenceJob
	if err := json.Unmarshal(body, &j); err != nil {
		return err
	}
	c.mu.Lock()
	c.jobs = append(c.jobs, j)
	c.mu.Unlock()
	return nil
}

// jsonExtractor is a synthetic dump extractor: it reads a JSON array of
// {id,title,text} objects and renders one record each, exercising DumpProducer
// without coupling the shared package to a specific source.
func jsonExtractor(source, path string) ([]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(items))
	for _, it := range items {
		records = append(records, BuildRecord(source, it.ID, it.Title, "https://x/"+it.ID, it.Text, nil, nil))
	}
	return records, nil
}

// dumpServer serves a JSON dump with an ETag, answering 304 when the client
// replays it, and counts full-body downloads so the conditional-GET skip is
// observable.
func dumpServer(t *testing.T, body string) (*httptest.Server, *int) {
	t.Helper()
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "dump-v1" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		downloads++
		w.Header().Set("ETag", "dump-v1")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &downloads
}

func TestDumpProducerRunAndConditionalSkip(t *testing.T) {
	t.Parallel()
	srv, downloads := dumpServer(t, `[{"id":"1","title":"A","text":"alpha"},{"id":"2","title":"B","text":"beta"}]`)
	dir := t.TempDir()
	pub := &capturePublisher{}
	p, err := NewDumpProducer(DumpConfig{
		Source:       "synthetic",
		URL:          srv.URL + "/dump.json",
		Scope:        "synthetic dump",
		Extract:      jsonExtractor,
		MarkerPath:   filepath.Join(dir, "marker.json"),
		ManifestPath: filepath.Join(dir, "manifest.json"),
		MaxPriority:  5,
	}, pub, nil)
	if err != nil {
		t.Fatalf("NewDumpProducer: %v", err)
	}
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run (first): %v", err)
	}
	if stats.New != 2 || len(pub.jobs) != 2 {
		t.Fatalf("first run: stats=%+v jobs=%d, want 2/2", stats, len(pub.jobs))
	}
	// Second run: the dump is unchanged, so the conditional GET returns 304 and the
	// producer neither downloads the body nor publishes.
	stats, err = p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run (second): %v", err)
	}
	if stats.New != 0 || len(pub.jobs) != 2 {
		t.Errorf("second run published extra work: stats=%+v jobs=%d", stats, len(pub.jobs))
	}
	if *downloads != 1 {
		t.Errorf("dump body downloaded %d times, want 1 (second run 304s)", *downloads)
	}
}

func TestDumpProducerMaxItemsBound(t *testing.T) {
	t.Parallel()
	srv, _ := dumpServer(t, `[{"id":"1","title":"A","text":"a"},{"id":"2","title":"B","text":"b"},{"id":"3","title":"C","text":"c"}]`)
	dir := t.TempDir()
	pub := &capturePublisher{}
	p, err := NewDumpProducer(DumpConfig{
		Source: "synthetic", URL: srv.URL + "/dump.json", Scope: "s", Extract: jsonExtractor,
		ManifestPath: filepath.Join(dir, "manifest.json"), MaxPriority: 5, MaxItems: 2,
	}, pub, nil)
	if err != nil {
		t.Fatalf("NewDumpProducer: %v", err)
	}
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 2 {
		t.Errorf("published %d with MaxItems=2, want 2", stats.New)
	}
	if stats.Skipped != 1 {
		t.Errorf("deferred %d, want 1", stats.Skipped)
	}
}

func TestNewDumpProducerRejectsBadConfig(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	_, err := NewDumpProducer(DumpConfig{Source: "", URL: "http://x", Extract: jsonExtractor, MaxPriority: 1}, pub, nil)
	if err == nil {
		t.Error("accepted an empty source")
	}
	_, err = NewDumpProducer(DumpConfig{Source: "s", URL: "http://x", Extract: jsonExtractor, MaxPriority: 0}, pub, nil)
	if err == nil {
		t.Error("accepted a zero priority")
	}
}
