package wiki

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// defaultBaseURL is the Wikimedia dump mirror root.
const defaultBaseURL = "https://dumps.wikimedia.org"

// userAgent follows Wikimedia's User-Agent policy: a descriptive client name
// with version, project URL, and contact - generic library defaults risk a
// 403.
const userAgent = "truth-in-stream-wikisync/1.0 (https://github.com/verovec/truth-in-stream; bot) Go-http-client"

// DumpFiles locates a downloaded dump and its companion index on disk.
// Version is the dump's Last-Modified header value, recorded in sync state so
// a later run can tell which dump the corpus came from.
type DumpFiles struct {
	DumpPath  string
	IndexPath string
	Version   string
}

// Downloader fetches multistream dumps from a Wikimedia mirror. The zero
// value downloads from dumps.wikimedia.org; downloads are bounded by the
// caller's context, not a client timeout - a full dump takes minutes.
type Downloader struct {
	BaseURL string
}

// Fetch downloads the corpus's multistream dump and index into destDir and
// returns their paths. The /latest/ aliases are mutable, so the pair is
// rejected when the two files report different Last-Modified values - an
// index from one dump generation describes byte offsets in another. Files
// are written via a temp file and renamed, so a failed download never leaves
// a partial file behind under the final name.
func (d *Downloader) Fetch(ctx context.Context, corpus, destDir string) (DumpFiles, error) {
	dumpName := corpus + "-latest-pages-articles-multistream.xml.bz2"
	indexName := corpus + "-latest-pages-articles-multistream-index.txt.bz2"

	dumpPath := filepath.Join(destDir, dumpName)
	dumpVersion, err := d.download(ctx, corpus, dumpName, dumpPath)
	if err != nil {
		return DumpFiles{}, err
	}
	indexPath := filepath.Join(destDir, indexName)
	indexVersion, err := d.download(ctx, corpus, indexName, indexPath)
	if err != nil {
		return DumpFiles{}, err
	}
	if dumpVersion != "" && indexVersion != "" && dumpVersion != indexVersion {
		return DumpFiles{}, fmt.Errorf("wiki: dump (%s) and index (%s) come from different dump generations; retry", dumpVersion, indexVersion)
	}
	return DumpFiles{DumpPath: dumpPath, IndexPath: indexPath, Version: dumpVersion}, nil
}

// download streams one dump file to dest and returns its Last-Modified value.
func (d *Downloader) download(ctx context.Context, corpus, name, dest string) (string, error) {
	base := d.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	url := fmt.Sprintf("%s/%s/latest/%s", base, corpus, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("wiki: build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("wiki: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wiki: download %s: unexpected status %s", url, resp.Status)
	}

	if err := writeAtomic(dest, resp.Body); err != nil {
		return "", fmt.Errorf("wiki: download %s: %w", url, err)
	}
	return resp.Header.Get("Last-Modified"), nil
}

// writeAtomic copies r to dest via a temp file in the same directory and a
// final rename.
func writeAtomic(dest string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
