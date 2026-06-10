package wiki

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
