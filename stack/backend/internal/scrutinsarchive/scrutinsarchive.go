// Package scrutinsarchive is the Assemblee Nationale scrutins-archive producer:
// it conditionally downloads the per-legislature Scrutins.json.zip bulk archive,
// discovers each scrutin inside it, and publishes one self-contained job per
// scrutin to the scrutins queue. It implements the crawlnotify.Producer seam so
// it alerts like the rest of the ingestion fleet.
//
// The archive is the AN open-data bulk file
// (https://data.assemblee-nationale.fr/static/openData/repository/{legislature}/loi/scrutins/Scrutins.json.zip),
// under the Etalab open-data license (attribution "Assemblee nationale -
// data.assemblee-nationale.fr"); programmatic bulk download is permitted. The
// static host serves ETag and Last-Modified validators, so the producer persists
// them and replays them as If-None-Match / If-Modified-Since: an unchanged
// archive answers 304 and the run does no redundant work. The zip is streamed to
// a temp file (archive/zip needs a seekable source) and removed when the run
// ends, so the whole archive is never held in memory.
//
// The producer publishes; the scrutinsworker fleet parses and upserts. Each job
// carries the bare scrutin object so the worker re-wraps it for the existing
// votingrecord parser; idempotency is the worker's (person, scrutin) upsert key,
// so a redelivered job is safe.
package scrutinsarchive

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsjob"
)

// defaultURLTemplate is the AN open-data archive URL with a %s legislature slot.
const defaultURLTemplate = "https://data.assemblee-nationale.fr/static/openData/repository/%s/loi/scrutins/Scrutins.json.zip"

// defaultHTTPTimeout bounds the whole archive download so a stalled transfer
// fails the run rather than hanging the fleet. The archive is tens of MB, so the
// bound is generous.
const defaultHTTPTimeout = 5 * time.Minute

// maxArchiveBytes caps the downloaded archive so a misconfigured or hostile URL
// cannot fill the disk; the real archive is well under this.
const maxArchiveBytes = 512 << 20 // 512 MiB

// maxEntryBytes caps one decompressed scrutin entry read into memory. A scrutin
// JSON file is a few KB, so this is generous headroom while bounding a zip-bomb
// entry that would otherwise inflate to hundreds of MB before being rejected.
const maxEntryBytes = 16 << 20 // 16 MiB

// scrutinPriority is the queue priority every scrutin job is published at. All
// scrutins are equally valuable to the voting store, so there is no per-scrutin
// band; the producer publishes at the queue ceiling.
func scrutinPriority(maxPriority uint8) uint8 { return maxPriority }

// Publisher enqueues a marshaled scrutin job at a priority. The broker client
// adapter at the cmd layer satisfies it, so this package never imports the
// transport.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config configures a Client. Legislature is the AN legislature number
// interpolated into the archive URL (e.g. "17"); MarkerPath is where the
// conditional-GET validators persist between runs (empty disables the skip, so
// every run downloads); MaxPriority is the queue's priority ceiling; URLTemplate
// and HTTPClient override the endpoint and transport for tests. A nil HTTPClient
// falls back to a bounded default.
type Config struct {
	Legislature string
	MarkerPath  string
	MaxPriority uint8
	URLTemplate string
	HTTPClient  *http.Client
}

// Client downloads the scrutins archive conditionally and publishes scrutin jobs.
type Client struct {
	legislature string
	markerPath  string
	maxPriority uint8
	url         string
	httpClient  *http.Client
	pub         Publisher
	logger      *slog.Logger
}

// New builds a Client, failing fast on missing configuration. pub is the queue
// publisher the run publishes jobs through; a nil logger falls back to
// slog.Default.
func New(cfg Config, pub Publisher, logger *slog.Logger) (*Client, error) {
	if cfg.Legislature == "" {
		return nil, fmt.Errorf("scrutinsarchive: legislature is required")
	}
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("scrutinsarchive: max priority must be positive, got %d", cfg.MaxPriority)
	}
	if pub == nil {
		return nil, fmt.Errorf("scrutinsarchive: publisher is required")
	}
	tmpl := cfg.URLTemplate
	if tmpl == "" {
		tmpl = defaultURLTemplate
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		legislature: cfg.Legislature,
		markerPath:  cfg.MarkerPath,
		maxPriority: cfg.MaxPriority,
		url:         fmt.Sprintf(tmpl, cfg.Legislature),
		httpClient:  httpClient,
		pub:         pub,
		logger:      logger,
	}, nil
}

// Name identifies this producer to the alerting seam.
func (c *Client) Name() string { return "scrutins" }

// Scope is the human-readable description of what one run ingests.
func (c *Client) Scope() string { return "legislature:" + c.legislature }

// Run downloads the archive (conditional on the persisted validators), and when
// it has changed publishes one job per scrutin, then persists the new validators.
// When the archive is unchanged it publishes nothing and reports a fully-skipped
// run. Stats.New is the number of scrutins published this run; Stats.Skipped is
// the number left unfetched because the archive was unchanged. Run honors ctx
// cancellation.
func (c *Client) Run(ctx context.Context) (crawlnotify.Stats, error) {
	prev, err := loadMarker(c.markerPath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}

	archivePath, next, changed, err := c.download(ctx, prev)
	if err != nil {
		return crawlnotify.Stats{}, err
	}
	// Remove the temp archive whenever one was written, regardless of how the run
	// ends; download returns an empty path on a 304, for which Remove is a no-op.
	if archivePath != "" {
		defer func() { _ = os.Remove(archivePath) }()
	}
	if !changed {
		c.logger.InfoContext(ctx, "scrutins archive unchanged since last run, skipping",
			slog.String("legislature", c.legislature))
		return crawlnotify.Stats{}, nil
	}

	published, err := c.publishArchive(ctx, archivePath)
	if err != nil {
		return crawlnotify.Stats{New: published}, err
	}

	// Persist the validators only after every job is published, so an aborted
	// run re-downloads next time rather than skipping a partially-published
	// archive. A 200 with neither validator leaves the skip disabled - warn so the
	// operator knows every run will re-download (republishing is idempotent), and
	// skip writing an empty marker that could never produce a 304.
	if next.empty() {
		c.logger.WarnContext(ctx, "scrutins archive served no ETag or Last-Modified; unchanged-archive skip is disabled",
			slog.String("legislature", c.legislature))
	} else if err := saveMarker(c.markerPath, next); err != nil {
		return crawlnotify.Stats{New: published}, err
	}

	c.logger.InfoContext(ctx, "scrutins archive ingest finished",
		slog.String("legislature", c.legislature),
		slog.Int("published", published))
	return crawlnotify.Stats{New: published}, nil
}

// download issues a conditional GET against the archive, replaying prev's
// validators. It returns the temp-file path of the downloaded archive and the
// new validators when the server answers 200, or changed=false (and an empty
// path) when it answers 304 Not Modified. The caller removes the temp file.
func (c *Client) download(ctx context.Context, prev marker) (path string, next marker, changed bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return "", marker{}, false, fmt.Errorf("scrutinsarchive: build request: %w", err)
	}
	if !prev.empty() {
		if prev.ETag != "" {
			req.Header.Set("If-None-Match", prev.ETag)
		}
		if prev.LastModified != "" {
			req.Header.Set("If-Modified-Since", prev.LastModified)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", marker{}, false, fmt.Errorf("scrutinsarchive: download %q: %w", c.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return "", prev, false, nil
	case http.StatusOK:
	default:
		return "", marker{}, false, fmt.Errorf("scrutinsarchive: download %q: unexpected status %s", c.url, resp.Status)
	}

	tmpPath, err := c.streamToTemp(resp.Body)
	if err != nil {
		return "", marker{}, false, err
	}
	return tmpPath, marker{
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}, true, nil
}

// streamToTemp writes the response body to a temp file and returns its path,
// removing the file on any error so a failed download leaves nothing behind. The
// read is bounded so a runaway response cannot fill the disk.
func (c *Client) streamToTemp(body io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", "scrutins-*.zip")
	if err != nil {
		return "", fmt.Errorf("scrutinsarchive: create temp archive: %w", err)
	}
	tmpPath := tmp.Name()
	n, err := io.Copy(tmp, io.LimitReader(body, maxArchiveBytes+1))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("scrutinsarchive: write temp archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("scrutinsarchive: close temp archive: %w", err)
	}
	if n > maxArchiveBytes {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("scrutinsarchive: archive exceeds %d bytes", maxArchiveBytes)
	}
	return tmpPath, nil
}

// publishArchive opens the downloaded zip, discovers each scrutin JSON entry, and
// publishes one job per scrutin, returning the count published. It stops at the
// first error (a malformed entry or a publish failure) so a broken archive is
// never half-ingested without notice.
func (c *Client) publishArchive(ctx context.Context, archivePath string) (int, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, fmt.Errorf("scrutinsarchive: open archive: %w", err)
	}
	defer func() { _ = zr.Close() }()

	published := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.EqualFold(jsonExt(f.Name), ".json") {
			continue
		}
		if ctx.Err() != nil {
			return published, ctx.Err()
		}
		job, err := readJob(f)
		if err != nil {
			return published, err
		}
		body, err := json.Marshal(job)
		if err != nil {
			return published, fmt.Errorf("scrutinsarchive: encode scrutin job %q: %w", job.ID, err)
		}
		if err := c.pub.Publish(ctx, body, scrutinPriority(c.maxPriority)); err != nil {
			return published, fmt.Errorf("scrutinsarchive: publish scrutin job %q: %w", job.ID, err)
		}
		published++
	}
	return published, nil
}

// jsonExt returns the lowercase extension of name (including the dot), or "" when
// it has none. It avoids importing path/filepath for a single suffix check on a
// zip entry name, which always uses forward slashes.
func jsonExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || strings.ContainsAny(name[i:], "/") {
		return ""
	}
	return name[i:]
}

// archiveEntry is the {"scrutin": {...}} envelope every inner archive file wraps.
// The producer only needs the scrutin's uid and the raw inner object, so it reads
// just those rather than the full schema the votingrecord parser handles.
type archiveEntry struct {
	Scrutin struct {
		UID string `json:"uid"`
	} `json:"scrutin"`
}

// readJob reads one scrutin file from the archive into a ScrutinJob carrying the
// bare scrutin object and its uid. The file is read in full (it is one small
// scrutin) and the inner "scrutin" object is extracted so the job transports only
// the payload the worker re-wraps for the parser.
func readJob(f *zip.File) (scrutinsjob.ScrutinJob, error) {
	rc, err := f.Open()
	if err != nil {
		return scrutinsjob.ScrutinJob{}, fmt.Errorf("scrutinsarchive: open entry %q: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	if err != nil {
		return scrutinsjob.ScrutinJob{}, fmt.Errorf("scrutinsarchive: read entry %q: %w", f.Name, err)
	}
	if len(data) > maxEntryBytes {
		return scrutinsjob.ScrutinJob{}, fmt.Errorf("scrutinsarchive: entry %q exceeds %d bytes", f.Name, maxEntryBytes)
	}

	var envelope struct {
		Scrutin json.RawMessage `json:"scrutin"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return scrutinsjob.ScrutinJob{}, fmt.Errorf("scrutinsarchive: decode entry %q: %w", f.Name, err)
	}
	if len(envelope.Scrutin) == 0 {
		return scrutinsjob.ScrutinJob{}, fmt.Errorf("scrutinsarchive: entry %q has no scrutin object", f.Name)
	}

	var meta archiveEntry
	if err := json.Unmarshal(data, &meta); err != nil {
		return scrutinsjob.ScrutinJob{}, fmt.Errorf("scrutinsarchive: decode entry %q uid: %w", f.Name, err)
	}
	if meta.Scrutin.UID == "" {
		return scrutinsjob.ScrutinJob{}, fmt.Errorf("scrutinsarchive: entry %q has empty scrutin uid", f.Name)
	}

	return scrutinsjob.ScrutinJob{ID: meta.Scrutin.UID, Scrutin: envelope.Scrutin}, nil
}

// ensure Client satisfies the producer seam at compile time.
var _ crawlnotify.Producer = (*Client)(nil)
