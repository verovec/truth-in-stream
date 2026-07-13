package evidencesrc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// DefaultHTTPTimeout bounds a whole bulk-dump download so a stalled transfer
// fails the run rather than hanging the fleet. The dumps are hundreds of MB, so
// the bound is generous.
const DefaultHTTPTimeout = 20 * time.Minute

// MaxArchiveBytes caps a downloaded dump so a misconfigured or hostile URL cannot
// fill the disk; the real dumps (a few hundred MB at most) are well under this.
const MaxArchiveBytes = 2 << 30 // 2 GiB

// Publisher enqueues a marshaled job at a priority. The broker client adapter at
// the cmd layer satisfies it, so a producer package never imports the transport.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Extractor turns a downloaded archive (its temp-file path) into the run's
// records. It owns the wire format: a JSON array, a raw XML dump, a zip of
// per-record files - each source supplies its own. It is called only when the
// conditional GET reports the dump changed, so it never reads a stale file.
type Extractor func(source, archivePath string) ([]Record, error)

// DumpConfig configures a [DumpProducer]. Source is the connector name every
// record carries and the run alerts key off; URL is the bulk-dump endpoint;
// Scope is the human-readable run description; Extract parses the downloaded
// archive; MarkerPath and ManifestPath persist the conditional-GET validators and
// the per-identifier fingerprints (empty disables that checkpoint); MaxPriority is
// the queue's priority ceiling; MaxItems bounds the records published in one run
// (0 = unbounded), a backfill safety valve; HTTPClient overrides the transport for
// tests.
type DumpConfig struct {
	Source       string
	URL          string
	Scope        string
	Extract      Extractor
	MarkerPath   string
	ManifestPath string
	MaxPriority  uint8
	MaxItems     int
	HTTPClient   *http.Client
}

// DumpProducer downloads a bulk open-data dump conditionally, diffs it against
// the persisted manifest, and publishes one connector.EvidenceJob per chunk of
// each new or changed record. It implements crawlnotify.Producer so it runs
// through the shared scheduler and alert wiring like every other source.
type DumpProducer struct {
	cfg        DumpConfig
	httpClient *http.Client
	pub        Publisher
	logger     *slog.Logger
}

// NewDumpProducer builds a DumpProducer, failing fast on missing configuration so
// a misconfigured run never publishes nothing silently.
func NewDumpProducer(cfg DumpConfig, pub Publisher, logger *slog.Logger) (*DumpProducer, error) {
	switch {
	case cfg.Source == "":
		return nil, fmt.Errorf("evidencesrc: source is required")
	case cfg.URL == "":
		return nil, fmt.Errorf("evidencesrc %q: url is required", cfg.Source)
	case cfg.Extract == nil:
		return nil, fmt.Errorf("evidencesrc %q: extractor is required", cfg.Source)
	case cfg.MaxPriority < 1:
		return nil, fmt.Errorf("evidencesrc %q: max priority must be positive, got %d", cfg.Source, cfg.MaxPriority)
	case pub == nil:
		return nil, fmt.Errorf("evidencesrc %q: publisher is required", cfg.Source)
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DumpProducer{cfg: cfg, httpClient: httpClient, pub: pub, logger: logger}, nil
}

// Name identifies this producer to the alerting seam and matches the connector
// descriptor Name, so the scheduler's enable/cron config and the run alerts agree.
func (p *DumpProducer) Name() string { return p.cfg.Source }

// Scope is the human-readable description of what one run ingests.
func (p *DumpProducer) Scope() string { return p.cfg.Scope }

// Run downloads the dump (conditional on the persisted validators), and when it
// has changed diffs every record against the manifest and publishes each new or
// changed record, then persists the new manifest and validators. When the dump is
// unchanged it publishes nothing and reports a fully-skipped run. Stats.New counts
// records published this run; Stats.Skipped counts records left unpublished
// (unchanged, or deferred by the backfill bound). Run honors ctx cancellation.
func (p *DumpProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	prevMarker, err := LoadMarker(p.cfg.MarkerPath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}
	prevManifest, err := LoadManifest(p.cfg.ManifestPath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}

	archivePath, nextMarker, changed, err := p.download(ctx, prevMarker)
	if err != nil {
		return crawlnotify.Stats{}, err
	}
	if archivePath != "" {
		defer func() { _ = os.Remove(archivePath) }()
	}
	if !changed {
		p.logger.InfoContext(ctx, "dump unchanged since last run, skipping", slog.String("source", p.cfg.Source))
		return crawlnotify.Stats{}, nil
	}

	records, err := p.cfg.Extract(p.cfg.Source, archivePath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}

	stats, nextManifest, err := p.publishRecords(ctx, records, prevManifest)
	if err != nil {
		return stats, err
	}

	// Persist the manifest and validators only after every job is published, so an
	// aborted run re-downloads and re-diffs next time rather than recording progress
	// it did not publish.
	if err := SaveManifest(p.cfg.ManifestPath, nextManifest); err != nil {
		return stats, err
	}
	if nextMarker.Empty() {
		p.logger.WarnContext(ctx, "dump served no ETag or Last-Modified; unchanged-dump skip is disabled",
			slog.String("source", p.cfg.Source))
	} else if err := SaveMarker(p.cfg.MarkerPath, nextMarker); err != nil {
		return stats, err
	}

	p.logger.InfoContext(ctx, "dump ingest finished",
		slog.String("source", p.cfg.Source),
		slog.Int("published", stats.New), slog.Int("unchanged_or_deferred", stats.Skipped))
	return stats, nil
}

// publishRecords runs the manifest diff over the extracted records, publishing
// only the new or changed ones and building the next manifest (the full
// fingerprint snapshot of this dump). It stops at the first publish error so a
// broken run never records progress it did not publish.
func (p *DumpProducer) publishRecords(ctx context.Context, records []Record, prev *Manifest) (crawlnotify.Stats, *Manifest, error) {
	next := NewManifest()
	var stats crawlnotify.Stats
	for _, rec := range records {
		if ctx.Err() != nil {
			return stats, next, ctx.Err()
		}
		if !prev.Changed(rec.ExternalID, rec.Fingerprint) {
			// Unchanged since last run: carry its fingerprint forward (it is already in
			// the corpus) and skip republishing.
			next.Set(rec.ExternalID, rec.Fingerprint)
			stats.Skipped++
			continue
		}
		if p.cfg.MaxItems > 0 && stats.New >= p.cfg.MaxItems {
			// Backfill bound reached: defer this changed record by NOT recording its
			// fingerprint, so the next run sees it as still-changed and publishes it
			// rather than skipping it forever. The manifest tracks only what has been
			// published, so a bounded run is resumable.
			stats.Skipped++
			continue
		}
		if err := p.publishRecord(ctx, rec); err != nil {
			return stats, next, err
		}
		next.Set(rec.ExternalID, rec.Fingerprint)
		stats.New++
	}
	return stats, next, nil
}

// publishRecord publishes every chunk of a record as a validated evidence job at
// the queue's priority ceiling. All chunks of a record are equally valuable, so
// there is no per-chunk band.
func (p *DumpProducer) publishRecord(ctx context.Context, rec Record) error {
	for _, job := range rec.Jobs {
		if err := job.Validate(); err != nil {
			return fmt.Errorf("evidencesrc %q: invalid evidence job for %q: %w", p.cfg.Source, rec.ExternalID, err)
		}
		body, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("evidencesrc %q: encode evidence job for %q: %w", p.cfg.Source, rec.ExternalID, err)
		}
		if err := p.pub.Publish(ctx, body, p.cfg.MaxPriority); err != nil {
			return fmt.Errorf("evidencesrc %q: publish evidence job for %q: %w", p.cfg.Source, rec.ExternalID, err)
		}
	}
	return nil
}

// download issues a conditional GET against the dump, replaying prev's
// validators. It returns the temp-file path of the downloaded dump and the new
// validators when the server answers 200, or changed=false (and an empty path)
// when it answers 304 Not Modified. The caller removes the temp file.
func (p *DumpProducer) download(ctx context.Context, prev Marker) (path string, next Marker, changed bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.URL, nil)
	if err != nil {
		return "", Marker{}, false, fmt.Errorf("evidencesrc %q: build request: %w", p.cfg.Source, err)
	}
	if !prev.Empty() {
		if prev.ETag != "" {
			req.Header.Set("If-None-Match", prev.ETag)
		}
		if prev.LastModified != "" {
			req.Header.Set("If-Modified-Since", prev.LastModified)
		}
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", Marker{}, false, fmt.Errorf("evidencesrc %q: download %q: %w", p.cfg.Source, p.cfg.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return "", prev, false, nil
	case http.StatusOK:
	default:
		return "", Marker{}, false, fmt.Errorf("evidencesrc %q: download %q: unexpected status %s", p.cfg.Source, p.cfg.URL, resp.Status)
	}

	tmpPath, err := p.streamToTemp(resp.Body)
	if err != nil {
		return "", Marker{}, false, err
	}
	return tmpPath, Marker{
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}, true, nil
}

// streamToTemp writes the response body to a temp file and returns its path,
// removing the file on any error so a failed download leaves nothing behind. The
// read is bounded so a runaway response cannot fill the disk.
func (p *DumpProducer) streamToTemp(body io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", p.cfg.Source+"-*.dump")
	if err != nil {
		return "", fmt.Errorf("evidencesrc %q: create temp dump: %w", p.cfg.Source, err)
	}
	tmpPath := tmp.Name()
	n, err := io.Copy(tmp, io.LimitReader(body, MaxArchiveBytes+1))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("evidencesrc %q: write temp dump: %w", p.cfg.Source, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("evidencesrc %q: close temp dump: %w", p.cfg.Source, err)
	}
	if n > MaxArchiveBytes {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("evidencesrc %q: dump exceeds %d bytes", p.cfg.Source, MaxArchiveBytes)
	}
	return tmpPath, nil
}

// ensure DumpProducer satisfies the producer seam at compile time.
var _ crawlnotify.Producer = (*DumpProducer)(nil)
