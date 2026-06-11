package wiki

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// defaultBaseURL is the Wikimedia dump mirror root.
const defaultBaseURL = "https://dumps.wikimedia.org"

// userAgent follows Wikimedia's User-Agent policy: a descriptive client name
// with version, project URL, and contact - generic library defaults risk a
// 403.
const userAgent = "truth-in-stream-wikisync/1.0 (https://github.com/verovec/truth-in-stream; bot) Go-http-client"

// versionSuffix names the sidecar written next to each downloaded file. It
// records the file's Last-Modified so a later run can issue a conditional
// request and reuse the bytes when the mirror has not published a newer dump.
const versionSuffix = ".last-modified"

// DumpFiles locates a downloaded dump and its companion index on disk.
// Version is the dump's Last-Modified header value, recorded in sync state so
// a later run can tell which dump the corpus came from. Reused is true when
// both files were already present and current, so no bytes were re-downloaded.
type DumpFiles struct {
	DumpPath  string
	IndexPath string
	Version   string
	Reused    bool
}

// Downloader fetches multistream dumps from a Wikimedia mirror. The zero
// value downloads from dumps.wikimedia.org; downloads are bounded by the
// caller's context, not a client timeout - a full dump takes minutes.
type Downloader struct {
	BaseURL string
}

// Fetch downloads the corpus's multistream dump and index into destDir and
// returns their paths. When a complete copy with a recorded version is already
// on disk, each file is fetched conditionally (If-Modified-Since) and reused on
// a 304, so a re-run skips re-downloading hundreds of megabytes. The /latest/
// aliases are mutable, so the pair is rejected when the two files report
// different Last-Modified values - an index from one dump generation describes
// byte offsets in another. Downloads are written via a temp file and renamed,
// so a failed download never leaves a partial file behind under the final name.
func (d *Downloader) Fetch(ctx context.Context, corpus, destDir string) (DumpFiles, error) {
	dumpName := corpus + "-latest-pages-articles-multistream.xml.bz2"
	indexName := corpus + "-latest-pages-articles-multistream-index.txt.bz2"

	dumpPath := filepath.Join(destDir, dumpName)
	dumpVersion, dumpReused, err := d.download(ctx, corpus, dumpName, dumpPath)
	if err != nil {
		return DumpFiles{}, err
	}
	indexPath := filepath.Join(destDir, indexName)
	indexVersion, indexReused, err := d.download(ctx, corpus, indexName, indexPath)
	if err != nil {
		return DumpFiles{}, err
	}
	if dumpVersion != "" && indexVersion != "" && dumpVersion != indexVersion {
		return DumpFiles{}, fmt.Errorf("wiki: dump (%s) and index (%s) come from different dump generations; retry", dumpVersion, indexVersion)
	}
	return DumpFiles{
		DumpPath:  dumpPath,
		IndexPath: indexPath,
		Version:   dumpVersion,
		Reused:    dumpReused && indexReused,
	}, nil
}

// download fetches one dump file into dest and returns its Last-Modified value.
// When a complete local copy with a recorded version exists, the request is
// made conditional with If-Modified-Since; a 304 reuses the local file (reused
// is true) and a 200 overwrites it atomically and records the new version.
func (d *Downloader) download(ctx context.Context, corpus, name, dest string) (version string, reused bool, err error) {
	base := d.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	url := fmt.Sprintf("%s/%s/latest/%s", base, corpus, name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, fmt.Errorf("wiki: build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)
	// The stored value is the server's own Last-Modified, already a valid
	// HTTP-date, so it is replayed verbatim.
	prior := localVersion(dest)
	conditional := prior != ""
	if conditional {
		req.Header.Set("If-Modified-Since", prior)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("wiki: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A 304 carries no body and may omit Last-Modified, so the reused version is
	// the one already recorded on disk. Only trust it when we actually sent the
	// conditional header; an unsolicited 304 is a protocol violation and falls
	// through to the status check below.
	if resp.StatusCode == http.StatusNotModified && conditional {
		return prior, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("wiki: download %s: unexpected status %s", url, resp.Status)
	}

	if err := writeAtomic(dest, resp.Body); err != nil {
		return "", false, fmt.Errorf("wiki: download %s: %w", url, err)
	}
	version = resp.Header.Get("Last-Modified")
	if err := recordVersion(dest, version); err != nil {
		return "", false, fmt.Errorf("wiki: download %s: %w", url, err)
	}
	return version, false, nil
}

// localVersion returns the recorded Last-Modified for a previously downloaded
// file, or "" when the file is missing, empty, or has no recorded version - any
// of which forces a fresh download rather than reusing an incomplete copy.
func localVersion(dest string) string {
	fi, err := os.Stat(dest)
	if err != nil || fi.Size() == 0 {
		return ""
	}
	b, err := os.ReadFile(dest + versionSuffix)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// recordVersion writes the freshly downloaded file's Last-Modified to its
// sidecar so a later run can reuse the bytes. When the mirror sent no
// Last-Modified, the new bytes cannot be validated later, so any sidecar left
// from an earlier download is removed - otherwise it would describe a different
// generation than the bytes now on disk and the next run would reuse them under
// a stale version. With no sidecar, the next run re-downloads instead.
func recordVersion(dest, version string) error {
	sidecar := dest + versionSuffix
	if version == "" {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear stale version for %s: %w", dest, err)
		}
		return nil
	}
	if err := writeAtomic(sidecar, strings.NewReader(version)); err != nil {
		return fmt.Errorf("record version for %s: %w", dest, err)
	}
	return nil
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
