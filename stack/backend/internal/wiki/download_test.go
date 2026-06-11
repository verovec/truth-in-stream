package wiki

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newDumpServer(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()

	var agents sync.Map
	mux := http.NewServeMux()
	mux.HandleFunc("GET /simplewiki/latest/simplewiki-latest-pages-articles-multistream.xml.bz2", func(w http.ResponseWriter, r *http.Request) {
		agents.Store("dump", r.UserAgent())
		w.Header().Set("Last-Modified", "Mon, 01 Jun 2026 03:14:00 GMT")
		_, _ = w.Write([]byte("dump-bytes"))
	})
	mux.HandleFunc("GET /simplewiki/latest/simplewiki-latest-pages-articles-multistream-index.txt.bz2", func(w http.ResponseWriter, r *http.Request) {
		agents.Store("index", r.UserAgent())
		w.Header().Set("Last-Modified", "Mon, 01 Jun 2026 03:14:00 GMT")
		_, _ = w.Write([]byte("index-bytes"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &agents
}

func TestDownloaderFetch(t *testing.T) {
	t.Parallel()

	srv, agents := newDumpServer(t)
	dir := t.TempDir()

	d := Downloader{BaseURL: srv.URL}
	files, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	dump, err := os.ReadFile(files.DumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if string(dump) != "dump-bytes" {
		t.Errorf("dump content = %q, want %q", dump, "dump-bytes")
	}
	index, err := os.ReadFile(files.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if string(index) != "index-bytes" {
		t.Errorf("index content = %q, want %q", index, "index-bytes")
	}

	if got := filepath.Base(files.DumpPath); got != "simplewiki-latest-pages-articles-multistream.xml.bz2" {
		t.Errorf("dump file name = %q", got)
	}
	if files.Version != "Mon, 01 Jun 2026 03:14:00 GMT" {
		t.Errorf("Version = %q, want the Last-Modified value", files.Version)
	}

	for _, key := range []string{"dump", "index"} {
		v, ok := agents.Load(key)
		if !ok {
			t.Fatalf("%s request never hit the server", key)
		}
		ua := v.(string)
		// Wikimedia's User-Agent policy: a descriptive client name with
		// version and contact info, never a bare library default.
		if !strings.Contains(ua, "truth-in-stream") || !strings.Contains(ua, "github.com/verovec/truth-in-stream") {
			t.Errorf("%s User-Agent %q not policy-compliant", key, ua)
		}
	}
}

func TestDownloaderFetchRejectsTornDumpIndexPair(t *testing.T) {
	t.Parallel()

	// The /latest/ aliases are mutable; if the mirror publishes a new dump
	// between the two downloads, the index's offsets describe a different
	// file. Mismatched Last-Modified values must fail the fetch.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /simplewiki/latest/simplewiki-latest-pages-articles-multistream.xml.bz2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "Mon, 01 Jun 2026 03:14:00 GMT")
		_, _ = w.Write([]byte("dump-bytes"))
	})
	mux.HandleFunc("GET /simplewiki/latest/simplewiki-latest-pages-articles-multistream-index.txt.bz2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", "Mon, 08 Jun 2026 03:14:00 GMT")
		_, _ = w.Write([]byte("index-bytes"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	d := Downloader{BaseURL: srv.URL}
	if _, err := d.Fetch(t.Context(), "simplewiki", t.TempDir()); err == nil {
		t.Fatal("Fetch accepted a dump/index pair from different dump generations, want error")
	}
}

// conditionalDumpServer is a stub Wikimedia mirror that honors conditional
// GETs. It serves a dump and index whose Last-Modified the test can advance,
// and counts full-body responses so a test can prove a run reused the local
// copy (304) instead of re-downloading (200).
type conditionalDumpServer struct {
	*httptest.Server
	mu           sync.Mutex
	lastModified time.Time
	dumpBody     string
	indexBody    string
	bodyServed   int // 200 responses that streamed a body
	notModified  int // 304 responses
}

func newConditionalDumpServer(t *testing.T) *conditionalDumpServer {
	t.Helper()

	s := &conditionalDumpServer{
		lastModified: time.Date(2026, time.June, 1, 3, 14, 0, 0, time.UTC),
		dumpBody:     "dump-bytes-v1",
		indexBody:    "index-bytes-v1",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /simplewiki/latest/{file}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		var body string
		switch r.PathValue("file") {
		case "simplewiki-latest-pages-articles-multistream.xml.bz2":
			body = s.dumpBody
		case "simplewiki-latest-pages-articles-multistream-index.txt.bz2":
			body = s.indexBody
		default:
			http.NotFound(w, r)
			return
		}

		// 304 when the client's recorded version is at or after the current
		// generation - the local copy is still current.
		if ims := r.Header.Get("If-Modified-Since"); ims != "" {
			if since, err := http.ParseTime(ims); err == nil && !s.lastModified.After(since) {
				s.notModified++
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		s.bodyServed++
		w.Header().Set("Last-Modified", s.lastModified.UTC().Format(http.TimeFormat))
		_, _ = io.WriteString(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.Server = srv
	return s
}

func (s *conditionalDumpServer) counts() (served, notModified int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodyServed, s.notModified
}

func TestDownloaderFetchReusesPresentDump(t *testing.T) {
	t.Parallel()

	srv := newConditionalDumpServer(t)
	dir := t.TempDir()
	d := Downloader{BaseURL: srv.URL}

	first, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if first.Reused {
		t.Fatal("first Fetch into an empty dir reported reuse")
	}
	if served, _ := srv.counts(); served != 2 {
		t.Fatalf("first Fetch streamed %d bodies, want 2 (dump + index)", served)
	}

	second, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if !second.Reused {
		t.Error("second Fetch did not reuse the dump already present on disk")
	}
	if second.Version != first.Version {
		t.Errorf("reused Version = %q, want %q", second.Version, first.Version)
	}

	served, notModified := srv.counts()
	if served != 2 {
		t.Errorf("second Fetch re-downloaded (total bodies served = %d, want 2)", served)
	}
	if notModified != 2 {
		t.Errorf("second Fetch issued %d conditional 304 requests, want 2", notModified)
	}
	if got, _ := os.ReadFile(second.DumpPath); string(got) != "dump-bytes-v1" {
		t.Errorf("reused dump content = %q, want %q", got, "dump-bytes-v1")
	}
}

func TestDownloaderFetchRefreshesStaleDump(t *testing.T) {
	t.Parallel()

	srv := newConditionalDumpServer(t)
	dir := t.TempDir()
	d := Downloader{BaseURL: srv.URL}

	if _, err := d.Fetch(t.Context(), "simplewiki", dir); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}

	// The /latest/ alias now points at a newer dump generation; the conditional
	// request must refresh rather than pin the corpus to the stale local copy.
	srv.mu.Lock()
	srv.lastModified = time.Date(2026, time.June, 8, 3, 14, 0, 0, time.UTC)
	srv.dumpBody = "dump-bytes-v2"
	srv.indexBody = "index-bytes-v2"
	srv.mu.Unlock()

	second, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if second.Reused {
		t.Error("second Fetch reused a stale dump instead of refreshing")
	}
	if got, _ := os.ReadFile(second.DumpPath); string(got) != "dump-bytes-v2" {
		t.Errorf("dump not refreshed: content = %q, want %q", got, "dump-bytes-v2")
	}
	if second.Version != "Mon, 08 Jun 2026 03:14:00 GMT" {
		t.Errorf("Version = %q, want the refreshed Last-Modified", second.Version)
	}
}

func TestDownloaderFetchDoesNotReuseIncompleteDump(t *testing.T) {
	t.Parallel()

	dumpName := "simplewiki-latest-pages-articles-multistream.xml.bz2"
	tests := []struct {
		name  string
		setup func(t *testing.T, dir, dumpPath string)
	}{
		{
			// A zero-byte file from an interrupted download must not be reused,
			// even if a stale version sidecar survives next to it.
			name: "zero-byte dump",
			setup: func(t *testing.T, _, dumpPath string) {
				t.Helper()
				if err := os.WriteFile(dumpPath, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dumpPath+".last-modified", []byte("Mon, 01 Jun 2026 03:14:00 GMT"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// A leftover dump with no recorded version cannot be validated as a
			// matched generation, so it must be re-downloaded, not skipped.
			name: "dump without recorded version",
			setup: func(t *testing.T, _, dumpPath string) {
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

			srv := newConditionalDumpServer(t)
			dir := t.TempDir()
			tc.setup(t, dir, filepath.Join(dir, dumpName))

			d := Downloader{BaseURL: srv.URL}
			files, err := d.Fetch(t.Context(), "simplewiki", dir)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if files.Reused {
				t.Error("Fetch reused an incomplete local dump")
			}
			if got, _ := os.ReadFile(files.DumpPath); string(got) != "dump-bytes-v1" {
				t.Errorf("dump not freshly downloaded: content = %q, want %q", got, "dump-bytes-v1")
			}
		})
	}
}

func TestDownloaderFetchDropsStaleSidecarWhenVersionMissing(t *testing.T) {
	t.Parallel()

	// A mirror that always returns fresh bytes with NO Last-Modified header.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /simplewiki/latest/{file}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "fresh-"+r.PathValue("file"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	dumpPath := filepath.Join(dir, "simplewiki-latest-pages-articles-multistream.xml.bz2")
	// Seed a leftover dump and an old version sidecar from an earlier run.
	if err := os.WriteFile(dumpPath, []byte("old-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dumpPath+".last-modified", []byte("Mon, 01 Jun 2026 03:14:00 GMT"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := Downloader{BaseURL: srv.URL}
	files, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if files.Reused {
		t.Error("Fetch reported reuse after an unversioned re-download")
	}
	// The stale sidecar must be gone, so the bytes on disk are never reused
	// under a version that does not describe them.
	if _, err := os.Stat(dumpPath + ".last-modified"); !os.IsNotExist(err) {
		t.Errorf("stale version sidecar survived an unversioned download (stat err = %v)", err)
	}
	if got, _ := os.ReadFile(dumpPath); string(got) != "fresh-simplewiki-latest-pages-articles-multistream.xml.bz2" {
		t.Errorf("dump not refreshed: content = %q", got)
	}

	// A second run must re-download rather than silently reuse the unverified
	// bytes under the dropped version.
	second, err := d.Fetch(t.Context(), "simplewiki", dir)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if second.Reused {
		t.Error("second Fetch reused bytes that carry no recorded version")
	}
}

func TestDownloaderFetchHTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	d := Downloader{BaseURL: srv.URL}
	if _, err := d.Fetch(t.Context(), "simplewiki", t.TempDir()); err == nil {
		t.Fatal("Fetch succeeded against a 404 server, want error")
	}
}

func TestDownloaderFetchLeavesNoPartialFileOnError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
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
