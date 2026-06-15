package wiki

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// defaultBaseURL is the Wikimedia dump mirror root.
const defaultBaseURL = "https://dumps.wikimedia.org"

// userAgent follows Wikimedia's User-Agent policy: a descriptive client name
// with version, project URL, and contact - generic library defaults risk a
// 403.
const userAgent = "truth-in-stream-wikisync/1.0 (https://github.com/verovec/truth-in-stream; bot) Go-http-client"

// versionSuffix names the sidecar written next to each downloaded file. It
// records the dump generation (a YYYYMMDD date) the local bytes came from, so a
// later run can reuse them when the mirror's newest generation is unchanged.
const versionSuffix = ".version"

// generationPattern matches the dated generation directories (e.g. 20260601/)
// in a corpus's autoindex listing.
var generationPattern = regexp.MustCompile(`href="(\d{8})/"`)

// DumpFiles locates a downloaded dump and its companion index on disk.
// Version is the dump generation (a YYYYMMDD date) the files came from, recorded
// in sync state so a later run can tell which generation the corpus came from.
// Reused is true when both files were already present for that generation, so no
// bytes were re-downloaded.
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

// Fetch resolves the newest complete dump generation and downloads that
// generation's multistream dump and index into destDir. The dump and index are
// fetched from the dated, immutable generation directory rather than the mutable
// /latest/ alias, so the two files are guaranteed to come from the same
// generation by construction - a dump and an index whose byte offsets disagree
// can never be paired. When a copy from the same generation is already on disk it
// is reused, so a re-run skips re-downloading hundreds of megabytes. Downloads
// are written via a temp file and renamed, so a failed download never leaves a
// partial file behind under the final name.
func (d *Downloader) Fetch(ctx context.Context, corpus, destDir string) (DumpFiles, error) {
	date, err := d.resolveGeneration(ctx, corpus)
	if err != nil {
		return DumpFiles{}, err
	}

	dumpPath := filepath.Join(destDir, localDumpName(corpus))
	dumpReused, err := d.download(ctx, d.fileURL(corpus, date, dumpFileName(corpus, date)), dumpPath, date)
	if err != nil {
		return DumpFiles{}, err
	}
	indexPath := filepath.Join(destDir, localIndexName(corpus))
	indexReused, err := d.download(ctx, d.fileURL(corpus, date, indexFileName(corpus, date)), indexPath, date)
	if err != nil {
		return DumpFiles{}, err
	}
	return DumpFiles{
		DumpPath:  dumpPath,
		IndexPath: indexPath,
		Version:   date,
		Reused:    dumpReused && indexReused,
	}, nil
}

// resolveGeneration finds the newest dump generation whose multistream dump and
// index are both published. The /<corpus>/ autoindex lists every generation as a
// dated directory, but the newest one can still be mid-run with its multistream
// files absent, so each candidate is confirmed with a HEAD on both files and the
// search falls back to the previous generation until a complete pair is found.
func (d *Downloader) resolveGeneration(ctx context.Context, corpus string) (string, error) {
	listURL := fmt.Sprintf("%s/%s/", d.baseURL(), corpus)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return "", fmt.Errorf("wiki: build request for %s: %w", listURL, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("wiki: list generations %s: %w", listURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wiki: list generations %s: unexpected status %s", listURL, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("wiki: read generation listing %s: %w", listURL, err)
	}

	dates := parseGenerations(body)
	if len(dates) == 0 {
		return "", fmt.Errorf("wiki: no dump generations listed for %s", corpus)
	}
	for _, date := range dates {
		ready, err := d.generationReady(ctx, corpus, date)
		if err != nil {
			return "", err
		}
		if ready {
			return date, nil
		}
	}
	return "", fmt.Errorf("wiki: no complete dump generation for %s among %d candidates", corpus, len(dates))
}

// parseGenerations extracts the dated generation directories from a corpus
// autoindex page, newest first. Duplicates are collapsed so a date appearing in
// both the href and the link text is counted once. Eight-digit YYYYMMDD strings
// sort chronologically as plain text, so a reverse sort yields newest-first.
func parseGenerations(listing []byte) []string {
	matches := generationPattern.FindAllSubmatch(listing, -1)
	seen := make(map[string]struct{}, len(matches))
	dates := make([]string, 0, len(matches))
	for _, m := range matches {
		date := string(m[1])
		if _, ok := seen[date]; ok {
			continue
		}
		seen[date] = struct{}{}
		dates = append(dates, date)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	return dates
}

// generationReady reports whether both the multistream dump and its index are
// published for the given generation, so the pair can be downloaded together.
func (d *Downloader) generationReady(ctx context.Context, corpus, date string) (bool, error) {
	for _, name := range []string{dumpFileName(corpus, date), indexFileName(corpus, date)} {
		ok, err := d.exists(ctx, d.fileURL(corpus, date, name))
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// exists issues a HEAD and reports whether the URL serves a file (200). A 404 is
// the expected "not published yet" answer for an in-progress generation; any
// other status is an error the caller should not paper over.
func (d *Downloader) exists(ctx context.Context, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, fmt.Errorf("wiki: build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("wiki: head %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("wiki: head %s: unexpected status %s", url, resp.Status)
	}
}

// download fetches one dump file into dest unless a copy from the same
// generation is already present. The dated source files are immutable, so a
// recorded generation matching the one being fetched proves the local bytes are
// current and the download is skipped. Otherwise the file is overwritten
// atomically and the generation recorded for the next run.
func (d *Downloader) download(ctx context.Context, url, dest, generation string) (reused bool, err error) {
	if localVersion(dest) == generation {
		return true, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("wiki: build request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("wiki: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("wiki: download %s: unexpected status %s", url, resp.Status)
	}

	if err := writeAtomic(dest, resp.Body); err != nil {
		return false, fmt.Errorf("wiki: download %s: %w", url, err)
	}
	if err := recordVersion(dest, generation); err != nil {
		return false, fmt.Errorf("wiki: download %s: %w", url, err)
	}
	return false, nil
}

func (d *Downloader) baseURL() string {
	if d.BaseURL != "" {
		return d.BaseURL
	}
	return defaultBaseURL
}

// fileURL builds the URL of a file inside a generation's dated directory.
func (d *Downloader) fileURL(corpus, date, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s", d.baseURL(), corpus, date, name)
}

// dumpFileName and indexFileName name the multistream dump and its index inside
// a generation's dated directory; the date is part of the published filename.
func dumpFileName(corpus, date string) string {
	return fmt.Sprintf("%s-%s-pages-articles-multistream.xml.bz2", corpus, date)
}

func indexFileName(corpus, date string) string {
	return fmt.Sprintf("%s-%s-pages-articles-multistream-index.txt.bz2", corpus, date)
}

// localDumpName and localIndexName are the stable on-disk cache names. The
// generation lives in the sidecar, not the filename, so a new generation
// overwrites the previous one in place rather than accumulating copies.
func localDumpName(corpus string) string {
	return corpus + "-pages-articles-multistream.xml.bz2"
}

func localIndexName(corpus string) string {
	return corpus + "-pages-articles-multistream-index.txt.bz2"
}

// localVersion returns the recorded generation for a previously downloaded
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

// recordVersion writes the freshly downloaded file's generation to its sidecar
// so a later run can reuse the bytes. The sidecar is written after the file is
// renamed into place, so its presence proves the bytes on disk are complete.
func recordVersion(dest, generation string) error {
	if err := writeAtomic(dest+versionSuffix, strings.NewReader(generation)); err != nil {
		return fmt.Errorf("record generation for %s: %w", dest, err)
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
