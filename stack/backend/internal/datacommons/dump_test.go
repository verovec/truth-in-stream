package datacommons

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// ndjsonDump is two newline-delimited ClaimReview objects, the shape of the
// DataCommons historical dump the one-shot importer reads.
const ndjsonDump = `{"claimReviewed":"Une affirmation vérifiée","datePublished":"2019-06-05","url":"https://factuel.afp.com/dump-1","author":{"name":"AFP Factuel","url":"https://factuel.afp.com/"},"reviewRating":{"alternateName":"Faux"}}
{"claimReviewed":"Une autre affirmation","datePublished":"2019-06-05","url":"https://www.lemonde.fr/dump-2","author":{"name":"Le Monde","url":"https://www.lemonde.fr/"},"reviewRating":{"alternateName":"Vrai"}}
`

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// TestRunContentEncodingGzipNotDoubleDecompressed guards the #9 fix: when the dump is
// served with Content-Encoding: gzip, net/http's transport already decompresses it and
// strips the header, so the client must NOT gunzip again (which would fail). Detection
// is by sniffing the body's magic bytes, so an already-decompressed body passes through.
func TestRunContentEncodingGzipNotDoubleDecompressed(t *testing.T) {
	t.Parallel()
	gz := gzipBytes(t, ndjsonDump)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip") // transport will transparently decode
		_, _ = w.Write(gz)
	}))
	defer srv.Close()

	c, err := New(Config{
		FeedURL:         srv.URL + "/dump.txt.gz", // .gz suffix must NOT force a second gunzip
		Format:          "ndjson",
		OutletAllowlist: []string{"factuel.afp.com", "lemonde.fr"},
		MaxPriority:     9,
		HTTPClient:      srv.Client(),
		Retry:           httpx.RetryConfig{MaxRetries: -1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run (must not double-decompress): %v", err)
	}
	if stats.Published != 2 {
		t.Fatalf("published = %d, want 2", stats.Published)
	}
}

func TestRunGzippedNDJSONDumpImport(t *testing.T) {
	t.Parallel()
	gz := gzipBytes(t, ndjsonDump)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(gz)
	}))
	defer srv.Close()

	c, err := New(Config{
		FeedURL:         srv.URL + "/fact_checks_20190605.txt.gz",
		Format:          "ndjson",
		OutletAllowlist: []string{"factuel.afp.com", "lemonde.fr"},
		MaxPriority:     9,
		HTTPClient:      srv.Client(),
		Retry:           httpx.RetryConfig{MaxRetries: -1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 2 {
		t.Fatalf("published = %d, want 2 from the gzipped NDJSON dump", stats.Published)
	}
	jobs := pub.jobs(t)
	byID := map[string]string{}
	for _, j := range jobs {
		byID[j.ID] = j.LiteralVerdict
	}
	if byID["https://factuel.afp.com/dump-1"] != string(domain.LiteralInaccurate) {
		t.Errorf("dump-1 verdict = %q, want inaccurate", byID["https://factuel.afp.com/dump-1"])
	}
	if byID["https://www.lemonde.fr/dump-2"] != string(domain.LiteralAccurate) {
		t.Errorf("dump-2 verdict = %q, want accurate", byID["https://www.lemonde.fr/dump-2"])
	}
}
