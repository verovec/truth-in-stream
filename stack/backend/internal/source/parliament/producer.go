package parliament

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

// defaultHTTPTimeout bounds a whole bulk-dump download so a stalled transfer fails
// the run rather than hanging the fleet. The dumps are hundreds of MB, so the bound
// is generous.
const defaultHTTPTimeout = 20 * time.Minute

// Publisher enqueues a marshaled job at a priority. The broker client adapter at
// the cmd layer satisfies it, so this package never imports the transport. The cmd
// layer binds the right queue (evidence.chunks for evidence datasets, scrutins.votes
// for the voting dataset).
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config configures a Producer. Dataset selects the dataset family; Legislature is
// interpolated into an AN dump URL (Senat rolling exports ignore it); MarkerPath
// persists the conditional-GET validators (empty disables the unchanged-dump skip);
// ManifestPath persists the per-identifier fingerprints for incremental diffing
// (empty disables the diff, so every record republishes); MaxPriority is the queue's
// priority ceiling; MaxItems bounds the records published in one run (0 = unbounded),
// a backfill safety valve; SinceYear bounds the Senat scrutins to recent sessions
// (0 = every session); URLTemplate and HTTPClient override the endpoint and transport
// for tests.
type Config struct {
	Dataset      string
	Legislature  string
	MarkerPath   string
	ManifestPath string
	MaxPriority  uint8
	MaxItems     int
	SinceYear    int
	URLTemplate  string
	HTTPClient   *http.Client
}

// Producer downloads a parliament dataset's bulk dump conditionally, diffs it
// against the persisted manifest, and publishes one job per new or changed record
// (evidence chunks, or a chamber-aware scrutin). It implements crawlnotify.Producer
// so it runs through the shared scheduler and alert wiring like every other source.
type Producer struct {
	dataset      string
	spec         datasetSpec
	legislature  string
	markerPath   string
	manifestPath string
	maxPriority  uint8
	maxItems     int
	sinceYear    int
	url          string
	httpClient   *http.Client
	scope        string
	pub          Publisher
	logger       *slog.Logger
}

// New builds a Producer, failing fast on an unknown dataset or missing
// configuration. pub is the queue publisher jobs are published through; a nil logger
// falls back to slog.Default.
func New(cfg Config, pub Publisher, logger *slog.Logger) (*Producer, error) {
	spec, err := lookupSpec(cfg.Dataset)
	if err != nil {
		return nil, err
	}
	if cfg.Legislature == "" {
		return nil, fmt.Errorf("parliament: legislature is required")
	}
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("parliament: max priority must be positive, got %d", cfg.MaxPriority)
	}
	if pub == nil {
		return nil, fmt.Errorf("parliament: publisher is required")
	}
	tmpl := cfg.URLTemplate
	if tmpl == "" {
		tmpl = spec.urlTemplate
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Producer{
		dataset:      cfg.Dataset,
		spec:         spec,
		legislature:  cfg.Legislature,
		markerPath:   cfg.MarkerPath,
		manifestPath: cfg.ManifestPath,
		maxPriority:  cfg.MaxPriority,
		maxItems:     cfg.MaxItems,
		sinceYear:    cfg.SinceYear,
		url:          buildURL(tmpl, cfg.Legislature),
		httpClient:   httpClient,
		scope:        spec.scope(cfg.Legislature),
		pub:          pub,
		logger:       logger,
	}, nil
}

// Name identifies this producer to the alerting seam and matches the connector
// descriptor Name, so the scheduler's enable/cron config and the run alerts agree.
func (p *Producer) Name() string { return p.spec.source }

// Scope is the human-readable description of what one run ingests.
func (p *Producer) Scope() string { return p.scope }

// Run downloads the dump (conditional on the persisted validators), and when it has
// changed diffs every record against the manifest and publishes each new or changed
// record, then persists the new manifest and validators. When the dump is unchanged
// it publishes nothing and reports a fully-skipped run. Stats.New counts records
// published this run; Stats.Skipped counts records left unpublished (unchanged, or
// deferred by the backfill bound). Run honors ctx cancellation.
func (p *Producer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	prevMarker, err := loadMarker(p.markerPath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}
	prevManifest, err := loadManifest(p.manifestPath)
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
		p.logger.InfoContext(ctx, "parliament dump unchanged since last run, skipping",
			slog.String("dataset", p.dataset))
		return crawlnotify.Stats{}, nil
	}

	items, err := p.collect(archivePath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}

	stats, nextManifest, err := p.publishItems(ctx, items, prevManifest)
	if err != nil {
		return stats, err
	}

	// Persist the manifest and validators only after every job is published, so an
	// aborted run re-downloads and re-diffs next time rather than recording progress
	// it did not publish.
	if err := saveManifest(p.manifestPath, nextManifest); err != nil {
		return stats, err
	}
	if nextMarker.empty() {
		p.logger.WarnContext(ctx, "parliament dump served no ETag or Last-Modified; unchanged-dump skip is disabled",
			slog.String("dataset", p.dataset))
	} else if err := saveMarker(p.markerPath, nextMarker); err != nil {
		return stats, err
	}

	p.logger.InfoContext(ctx, "parliament dump ingest finished",
		slog.String("dataset", p.dataset),
		slog.Int("published", stats.New), slog.Int("unchanged_or_deferred", stats.Skipped))
	return stats, nil
}

// pubItem is one publishable record: its stable id and content fingerprint (for the
// manifest diff) and the closure that publishes its bodies when it is new or changed.
type pubItem struct {
	id          string
	fingerprint string
	publish     func(ctx context.Context) error
}

// collect turns the downloaded archive into the run's publishable items, dispatching
// on the dataset target: evidence datasets yield one item per record (publishing its
// chunk jobs), the voting dataset yields one item per scrutin (publishing its
// chamber-aware scrutins job).
func (p *Producer) collect(archivePath string) ([]pubItem, error) {
	switch p.spec.target {
	case targetVoting:
		payloads, err := p.spec.extractVotes(archivePath, p.sinceYear)
		if err != nil {
			return nil, err
		}
		items := make([]pubItem, 0, len(payloads))
		for _, pl := range payloads {
			body := pl.body
			items = append(items, pubItem{id: pl.id, fingerprint: pl.fingerprint, publish: func(ctx context.Context) error {
				return p.pub.Publish(ctx, body, p.maxPriority)
			}})
		}
		return items, nil
	default:
		records, err := p.spec.extract(p.spec.source, archivePath)
		if err != nil {
			return nil, err
		}
		items := make([]pubItem, 0, len(records))
		for _, rec := range records {
			rec := rec
			items = append(items, pubItem{id: rec.externalID, fingerprint: rec.fingerprint, publish: func(ctx context.Context) error {
				return p.publishRecord(ctx, rec)
			}})
		}
		return items, nil
	}
}

// publishItems runs the manifest diff over the collected items, publishing only the
// new or changed ones and building the next manifest (the full fingerprint snapshot
// of this dump). It returns the run stats (New = records published, Skipped =
// unchanged or backfill-deferred records). It stops at the first publish error so a
// broken run never records progress it did not publish.
func (p *Producer) publishItems(ctx context.Context, items []pubItem, prev *Manifest) (crawlnotify.Stats, *Manifest, error) {
	next := newManifest()
	var stats crawlnotify.Stats
	for _, it := range items {
		if ctx.Err() != nil {
			return stats, next, ctx.Err()
		}
		if !prev.Changed(it.id, it.fingerprint) {
			// Unchanged since last run: carry its fingerprint forward (it is already in
			// the corpus) and skip republishing.
			next.Set(it.id, it.fingerprint)
			stats.Skipped++
			continue
		}
		if p.maxItems > 0 && stats.New >= p.maxItems {
			// Backfill bound reached: defer this changed record by NOT recording its
			// fingerprint, so the next run sees it as still-changed and publishes it
			// rather than skipping it forever. The manifest tracks only what has been
			// published, so a bounded run is resumable.
			stats.Skipped++
			continue
		}
		if err := it.publish(ctx); err != nil {
			return stats, next, err
		}
		next.Set(it.id, it.fingerprint)
		stats.New++
	}
	return stats, next, nil
}

// download issues a conditional GET against the dump, replaying prev's validators.
// It returns the temp-file path of the downloaded dump and the new validators when
// the server answers 200, or changed=false (and an empty path) when it answers 304
// Not Modified. The caller removes the temp file.
func (p *Producer) download(ctx context.Context, prev marker) (path string, next marker, changed bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return "", marker{}, false, fmt.Errorf("parliament: build request: %w", err)
	}
	if !prev.empty() {
		if prev.ETag != "" {
			req.Header.Set("If-None-Match", prev.ETag)
		}
		if prev.LastModified != "" {
			req.Header.Set("If-Modified-Since", prev.LastModified)
		}
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", marker{}, false, fmt.Errorf("parliament: download %q: %w", p.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return "", prev, false, nil
	case http.StatusOK:
	default:
		return "", marker{}, false, fmt.Errorf("parliament: download %q: unexpected status %s", p.url, resp.Status)
	}

	tmpPath, err := p.streamToTemp(resp.Body)
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
func (p *Producer) streamToTemp(body io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", "parliament-*.dump")
	if err != nil {
		return "", fmt.Errorf("parliament: create temp dump: %w", err)
	}
	tmpPath := tmp.Name()
	n, err := io.Copy(tmp, io.LimitReader(body, maxArchiveBytes+1))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("parliament: write temp dump: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("parliament: close temp dump: %w", err)
	}
	if n > maxArchiveBytes {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("parliament: dump exceeds %d bytes", maxArchiveBytes)
	}
	return tmpPath, nil
}

// publishRecord publishes every chunk of an evidence record as a validated evidence
// job at the queue's priority ceiling. All chunks of a record are equally valuable,
// so there is no per-chunk band.
func (p *Producer) publishRecord(ctx context.Context, rec record) error {
	for _, job := range rec.jobs {
		if err := job.Validate(); err != nil {
			return fmt.Errorf("parliament: invalid evidence job for %q: %w", rec.externalID, err)
		}
		body, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("parliament: encode evidence job for %q: %w", rec.externalID, err)
		}
		if err := p.pub.Publish(ctx, body, p.maxPriority); err != nil {
			return fmt.Errorf("parliament: publish evidence job for %q: %w", rec.externalID, err)
		}
	}
	return nil
}

// ensure Producer satisfies the producer seam at compile time.
var _ crawlnotify.Producer = (*Producer)(nil)
