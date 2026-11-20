package hatvp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/source/evidencesrc"
)

// httpTimeout bounds each HTTP request (the index download and each declaration
// fetch), so a stalled transfer fails the request rather than hanging the run.
const httpTimeout = 5 * time.Minute

// maxIndexBytes and maxDeclarationBytes bound the two reads into memory: the index
// is a single CSV (a few MB), each declaration a small XML. The bounds reject a
// runaway or hostile response.
const (
	maxIndexBytes       = 256 << 20 // 256 MiB
	maxDeclarationBytes = 16 << 20  // 16 MiB
)

// Publisher enqueues a marshaled job at a priority. The broker client adapter at
// the cmd layer satisfies it, so this package never imports the transport.
type Publisher = evidencesrc.Publisher

// Config configures a Producer. IndexURL is the CSV index; DossierBaseURL is the
// per-declaration XML base (both overridable for tests); MarkerPath persists the
// index conditional-GET validators; ManifestPath persists the per-declaration
// fingerprints; MaxPriority is the queue's priority ceiling; MaxItems bounds the
// declarations published in one run (0 = unbounded), a backfill safety valve;
// HTTPClient overrides the transport for tests.
type Config struct {
	IndexURL       string
	DossierBaseURL string
	MarkerPath     string
	ManifestPath   string
	MaxPriority    uint8
	MaxItems       int
	HTTPClient     *http.Client
}

// Producer diffs the HATVP CSV index conditionally and, for each new or changed
// published declaration, fetches its XML and publishes a structured summary as the
// generic connector.EvidenceJob. It implements crawlnotify.Producer so it runs
// through the shared scheduler and alert wiring like every other source.
type Producer struct {
	cfg        Config
	httpClient *http.Client
	pub        Publisher
	logger     *slog.Logger
}

// New builds a Producer, defaulting the endpoints and failing fast on missing
// configuration.
func New(cfg Config, pub Publisher, logger *slog.Logger) (*Producer, error) {
	if pub == nil {
		return nil, fmt.Errorf("hatvp: publisher is required")
	}
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("hatvp: max priority must be positive, got %d", cfg.MaxPriority)
	}
	if cfg.IndexURL == "" {
		cfg.IndexURL = IndexURL
	}
	if cfg.DossierBaseURL == "" {
		cfg.DossierBaseURL = DossierBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Producer{cfg: cfg, httpClient: cfg.HTTPClient, pub: pub, logger: logger}, nil
}

// Name identifies this producer to the alerting seam and matches the connector
// descriptor Name.
func (p *Producer) Name() string { return Source }

// Scope is the human-readable description of what one run ingests.
func (p *Producer) Scope() string { return "HATVP declarations d'interets" }

// Run downloads the index (conditional on the persisted validators), and when it
// has changed diffs every declaration row against the manifest, fetches and
// publishes each new or changed one, then persists the new manifest and
// validators. A declaration whose XML fetch fails is skipped without recording its
// fingerprint, so the next run retries it rather than dropping it. Stats.New counts
// declarations published; Stats.Skipped counts rows left unpublished (unchanged,
// deferred by the backfill bound, or fetch-failed). Run honors ctx cancellation.
func (p *Producer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	prevMarker, err := evidencesrc.LoadMarker(p.cfg.MarkerPath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}
	prevManifest, err := evidencesrc.LoadManifest(p.cfg.ManifestPath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}

	body, nextMarker, changed, err := p.downloadIndex(ctx, prevMarker)
	if err != nil {
		return crawlnotify.Stats{}, err
	}
	if !changed {
		p.logger.InfoContext(ctx, "hatvp index unchanged since last run, skipping")
		return crawlnotify.Stats{}, nil
	}

	rows, err := parseIndex(bytes.NewReader(body))
	if err != nil {
		return crawlnotify.Stats{}, err
	}

	stats, nextManifest, err := p.publishRows(ctx, rows, prevManifest)
	if err != nil {
		return stats, err
	}

	if err := evidencesrc.SaveManifest(p.cfg.ManifestPath, nextManifest); err != nil {
		return stats, err
	}
	if nextMarker.Empty() {
		p.logger.WarnContext(ctx, "hatvp index served no ETag or Last-Modified; unchanged-index skip is disabled")
	} else if err := evidencesrc.SaveMarker(p.cfg.MarkerPath, nextMarker); err != nil {
		return stats, err
	}

	p.logger.InfoContext(ctx, "hatvp ingest finished",
		slog.Int("published", stats.New), slog.Int("unchanged_deferred_or_failed", stats.Skipped))
	return stats, nil
}

// publishRows diffs each index row, fetches and publishes the new or changed
// declarations up to the backfill bound, and returns the run stats and the next
// manifest (the fingerprint snapshot of the published set).
func (p *Producer) publishRows(ctx context.Context, rows []indexRow, prev *evidencesrc.Manifest) (crawlnotify.Stats, *evidencesrc.Manifest, error) {
	next := evidencesrc.NewManifest()
	var stats crawlnotify.Stats
	for _, row := range rows {
		if ctx.Err() != nil {
			return stats, next, ctx.Err()
		}
		fp := row.fingerprint()
		if !prev.Changed(row.OpenDataFile, fp) {
			next.Set(row.OpenDataFile, fp)
			stats.Skipped++
			continue
		}
		if p.cfg.MaxItems > 0 && stats.New >= p.cfg.MaxItems {
			stats.Skipped++
			continue
		}
		decl, err := p.fetchDeclaration(ctx, row.OpenDataFile)
		if err != nil {
			// A single unfetchable declaration must not strand the whole run. Skip it
			// without recording its fingerprint so the next run retries it.
			p.logger.WarnContext(ctx, "hatvp declaration fetch failed, skipping",
				slog.String("file", row.OpenDataFile), slog.Any("err", err))
			stats.Skipped++
			continue
		}
		if strings.TrimSpace(row.URLDossier) == "" {
			// Provenance still resolves to the HATVP homepage, but a missing nominative
			// link is worth flagging: it usually means a malformed index row.
			p.logger.WarnContext(ctx, "hatvp index row has no url_dossier; provenance falls back to the homepage",
				slog.String("file", row.OpenDataFile))
		}
		if err := p.publishRecord(ctx, buildRecord(row, decl)); err != nil {
			return stats, next, err
		}
		next.Set(row.OpenDataFile, fp)
		stats.New++
	}
	return stats, next, nil
}

// publishRecord publishes every chunk of a record as a validated evidence job.
func (p *Producer) publishRecord(ctx context.Context, rec evidencesrc.Record) error {
	for _, job := range rec.Jobs {
		if err := job.Validate(); err != nil {
			return fmt.Errorf("hatvp: invalid evidence job for %q: %w", rec.ExternalID, err)
		}
		body, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("hatvp: encode evidence job for %q: %w", rec.ExternalID, err)
		}
		if err := p.pub.Publish(ctx, body, p.cfg.MaxPriority); err != nil {
			return fmt.Errorf("hatvp: publish evidence job for %q: %w", rec.ExternalID, err)
		}
	}
	return nil
}

// downloadIndex issues a conditional GET against the CSV index, replaying prev's
// validators. It returns the body and the new validators on 200, or changed=false
// on 304 Not Modified.
func (p *Producer) downloadIndex(ctx context.Context, prev evidencesrc.Marker) (body []byte, next evidencesrc.Marker, changed bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.IndexURL, nil)
	if err != nil {
		return nil, evidencesrc.Marker{}, false, fmt.Errorf("hatvp: build index request: %w", err)
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
		return nil, evidencesrc.Marker{}, false, fmt.Errorf("hatvp: download index %q: %w", p.cfg.IndexURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, prev, false, nil
	case http.StatusOK:
	default:
		return nil, evidencesrc.Marker{}, false, fmt.Errorf("hatvp: download index %q: unexpected status %s", p.cfg.IndexURL, resp.Status)
	}
	data, err := readLimited(resp.Body, maxIndexBytes)
	if err != nil {
		return nil, evidencesrc.Marker{}, false, fmt.Errorf("hatvp: read index: %w", err)
	}
	return data, evidencesrc.Marker{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}, true, nil
}

// fetchDeclaration downloads and parses one declaration XML by its index file
// name.
func (p *Producer) fetchDeclaration(ctx context.Context, file string) (declaration, error) {
	url := dossierURL(p.cfg.DossierBaseURL, file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return declaration{}, fmt.Errorf("hatvp: build declaration request: %w", err)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return declaration{}, fmt.Errorf("hatvp: fetch %q: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return declaration{}, fmt.Errorf("hatvp: fetch %q: unexpected status %s", url, resp.Status)
	}
	data, err := readLimited(resp.Body, maxDeclarationBytes)
	if err != nil {
		return declaration{}, fmt.Errorf("hatvp: read %q: %w", url, err)
	}
	return parseDeclaration(data)
}

// readLimited reads r fully under the byte bound, rejecting a response that
// exceeds it rather than buffering an unbounded body.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("hatvp: response exceeds %d bytes", limit)
	}
	return data, nil
}

// ensure Producer satisfies the producer seam at compile time.
var _ crawlnotify.Producer = (*Producer)(nil)
