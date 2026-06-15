package wiki

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// testCorpus is the corpus name the fake mirror and every test use.
const testCorpus = "simplewiki"

// generation is one dated dump the fake mirror publishes.
type generation struct {
	date      string
	dumpBody  string
	indexBody string
}

// dumpMirror is a fake Wikimedia mirror serving a dated, immutable dump layout:
// a /<corpus>/ autoindex of generation directories, and the multistream dump and
// index under each /<corpus>/<date>/. It counts full GET bodies served so a test
// can prove a run reused the local copy instead of re-downloading, and records
// the User-Agent of each request to assert policy compliance.
type dumpMirror struct {
	*httptest.Server
	corpus string

	mu     sync.Mutex
	gens   []generation
	served int
	agents map[string]string
}

func newDumpMirror(t *testing.T, gens ...generation) *dumpMirror {
	t.Helper()

	m := &dumpMirror{corpus: testCorpus, gens: gens, agents: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("GET /%s/{$}", testCorpus), m.handleListing)
	mux.HandleFunc(fmt.Sprintf("GET /%s/{date}/{file}", testCorpus), m.handleFile)
	mux.HandleFunc(fmt.Sprintf("HEAD /%s/{date}/{file}", testCorpus), m.handleFile)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m.Server = srv
	return m
}

func (m *dumpMirror) handleListing(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents["listing"] = r.UserAgent()

	var b strings.Builder
	b.WriteString("<html><body><pre>\n")
	for _, g := range m.gens {
		fmt.Fprintf(&b, `<a href="%s/">%s/</a>`+"\n", g.date, g.date)
	}
	b.WriteString("</pre></body></html>\n")
	_, _ = io.WriteString(w, b.String())
}

func (m *dumpMirror) handleFile(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[r.PathValue("file")] = r.UserAgent()

	date := r.PathValue("date")
	file := r.PathValue("file")
	for _, g := range m.gens {
		if g.date != date {
			continue
		}
		var body string
		switch file {
		case dumpFileName(m.corpus, date):
			body = g.dumpBody
		case indexFileName(m.corpus, date):
			body = g.indexBody
		default:
			http.NotFound(w, r)
			return
		}
		// An empty body models a generation that is listed but whose files are
		// not yet published, so the mirror 404s it the way a mid-run dump does.
		if body == "" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodHead {
			return
		}
		m.served++
		_, _ = io.WriteString(w, body)
		return
	}
	http.NotFound(w, r)
}

func (m *dumpMirror) bodiesServed() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.served
}

// prepend publishes a newer generation at the front of the listing, modeling
// the mirror advancing to a new dump between two runs.
func (m *dumpMirror) prepend(g generation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gens = append([]generation{g}, m.gens...)
}

func (m *dumpMirror) userAgent(t *testing.T, key string) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	ua, ok := m.agents[key]
	if !ok {
		t.Fatalf("%s request never hit the mirror", key)
	}
	return ua
}

func TestDownloaderFetch(t *testing.T) {
	t.Parallel()

	m := newDumpMirror(t, generation{date: "20260601", dumpBody: "dump-bytes", indexBody: "index-bytes"})
	dir := t.TempDir()

	d := Downloader{BaseURL: m.URL}
	files, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got, _ := os.ReadFile(files.DumpPath); string(got) != "dump-bytes" {
		t.Errorf("dump content = %q, want %q", got, "dump-bytes")
	}
	if got, _ := os.ReadFile(files.IndexPath); string(got) != "index-bytes" {
		t.Errorf("index content = %q, want %q", got, "index-bytes")
	}
	if got := filepath.Base(files.DumpPath); got != "simplewiki-pages-articles-multistream.xml.bz2" {
		t.Errorf("dump file name = %q", got)
	}
	if files.Version != "20260601" {
		t.Errorf("Version = %q, want the generation date", files.Version)
	}
	if files.Reused {
		t.Error("Fetch into an empty dir reported reuse")
	}

	for _, key := range []string{"listing", dumpFileName("simplewiki", "20260601"), indexFileName("simplewiki", "20260601")} {
		ua := m.userAgent(t, key)
		// Wikimedia's User-Agent policy: a descriptive client name with version
		// and contact info, never a bare library default.
		if !strings.Contains(ua, "truth-in-stream") || !strings.Contains(ua, "github.com/verovec/truth-in-stream") {
			t.Errorf("%s User-Agent %q not policy-compliant", key, ua)
		}
	}
}

func TestDownloaderFetchPicksNewestCompleteGeneration(t *testing.T) {
	t.Parallel()

	// The newest generation is mid-run: it is listed but its files are not yet
	// published (empty bodies, so the mirror 404s them). Fetch must fall back to
	// the previous, complete generation rather than fail.
	m := newDumpMirror(
		t,
		generation{date: "20260615"},
		generation{date: "20260601", dumpBody: "dump-v1", indexBody: "index-v1"},
	)

	d := Downloader{BaseURL: m.URL}
	files, err := d.Fetch(t.Context(), "simplewiki", t.TempDir())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if files.Version != "20260601" {
		t.Errorf("Version = %q, want the newest complete generation 20260601", files.Version)
	}
	if got, _ := os.ReadFile(files.DumpPath); string(got) != "dump-v1" {
		t.Errorf("dump content = %q, want the complete generation's bytes", got)
	}
}

func TestDownloaderFetchReusesPresentGeneration(t *testing.T) {
	t.Parallel()

	m := newDumpMirror(t, generation{date: "20260601", dumpBody: "dump-v1", indexBody: "index-v1"})
	dir := t.TempDir()
	d := Downloader{BaseURL: m.URL}

	first, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.Reused {
		t.Fatal("first Fetch into an empty dir reported reuse")
	}
	if served := m.bodiesServed(); served != 2 {
		t.Fatalf("first Fetch streamed %d bodies, want 2 (dump + index)", served)
	}

	second, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !second.Reused {
		t.Error("second Fetch did not reuse the generation already on disk")
	}
	if second.Version != first.Version {
		t.Errorf("reused Version = %q, want %q", second.Version, first.Version)
	}
	if served := m.bodiesServed(); served != 2 {
		t.Errorf("second Fetch re-downloaded (total bodies served = %d, want 2)", served)
	}
}

func TestDownloaderFetchRefreshesOnNewGeneration(t *testing.T) {
	t.Parallel()

	m := newDumpMirror(t, generation{date: "20260601", dumpBody: "dump-v1", indexBody: "index-v1"})
	dir := t.TempDir()
	d := Downloader{BaseURL: m.URL}

	if _, err := d.Fetch(t.Context(), "simplewiki", dir); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}

	// The mirror advances to a newer generation; the next run must refresh rather
	// than pin the corpus to the stale local copy.
	m.prepend(generation{date: "20260615", dumpBody: "dump-v2", indexBody: "index-v2"})

	second, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if second.Reused {
		t.Error("second Fetch reused a stale generation instead of refreshing")
	}
	if second.Version != "20260615" {
		t.Errorf("Version = %q, want the refreshed generation 20260615", second.Version)
	}
	if got, _ := os.ReadFile(second.DumpPath); string(got) != "dump-v2" {
		t.Errorf("dump not refreshed: content = %q, want %q", got, "dump-v2")
	}
}

func TestDownloaderFetchDoesNotReuseIncompleteLocalCopy(t *testing.T) {
	t.Parallel()

	dumpName := "simplewiki-pages-articles-multistream.xml.bz2"
	tests := []struct {
		name  string
		setup func(t *testing.T, dumpPath string)
	}{
		{
			// A zero-byte file from an interrupted download must not be reused,
			// even if a stale version sidecar survives next to it.
			name: "zero-byte dump",
			setup: func(t *testing.T, dumpPath string) {
				t.Helper()
				if err := os.WriteFile(dumpPath, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dumpPath+versionSuffix, []byte("20260601"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// A leftover dump with no recorded generation cannot be validated, so
			// it must be re-downloaded, not skipped.
			name: "dump without recorded version",
			setup: func(t *testing.T, dumpPath string) {
				t.Helper()
				if err := os.WriteFile(dumpPath, []byte("stale-partial-bytes"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := newDumpMirror(t, generation{date: "20260601", dumpBody: "dump-v1", indexBody: "index-v1"})
			dir := t.TempDir()
			tc.setup(t, filepath.Join(dir, dumpName))

			d := Downloader{BaseURL: m.URL}
			files, err := d.Fetch(t.Context(), "simplewiki", dir)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if files.Reused {
				t.Error("Fetch reused an incomplete local dump")
			}
			if got, _ := os.ReadFile(files.DumpPath); string(got) != "dump-v1" {
				t.Errorf("dump not freshly downloaded: content = %q, want %q", got, "dump-v1")
			}
		})
	}
}

func TestDownloaderFetchNoGenerationsListed(t *testing.T) {
	t.Parallel()

	m := newDumpMirror(t)

	d := Downloader{BaseURL: m.URL}
	if _, err := d.Fetch(t.Context(), "simplewiki", t.TempDir()); err == nil {
		t.Fatal("Fetch succeeded against a mirror that lists no generations, want error")
	}
}

func TestDownloaderFetchNoCompleteGeneration(t *testing.T) {
	t.Parallel()

	// A generation is listed but its files are not published, so no complete pair
	// exists and Fetch must fail rather than download a partial generation.
	m := newDumpMirror(t)
	m.gens = []generation{{date: "20260615"}}

	d := Downloader{BaseURL: m.URL}
	if _, err := d.Fetch(t.Context(), "simplewiki", t.TempDir()); err == nil {
		t.Fatal("Fetch succeeded with no complete generation published, want error")
	}
}

func TestDownloaderFetchListingHTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	d := Downloader{BaseURL: srv.URL}
	if _, err := d.Fetch(t.Context(), "simplewiki", t.TempDir()); err == nil {
		t.Fatal("Fetch succeeded against a 500 listing, want error")
	}
}

func TestDownloaderFetchLeavesNoPartialFileOnError(t *testing.T) {
	t.Parallel()

	const date = "20260601"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /simplewiki/{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<a href="%s/">%s/</a>`, date, date)
	})
	mux.HandleFunc("HEAD /simplewiki/{date}/{file}", func(_ http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("GET /simplewiki/{date}/{file}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	d := Downloader{BaseURL: srv.URL}
	if _, err := d.Fetch(t.Context(), "simplewiki", dir); err == nil {
		t.Fatal("Fetch succeeded on a truncated body, want error")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("partial download left final file %q", e.Name())
		}
	}
}
