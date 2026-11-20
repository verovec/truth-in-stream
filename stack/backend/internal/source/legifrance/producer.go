package legifrance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/source/evidencesrc"
)

// defaultMinInterval paces requests to the API to honor PISTE's per-second quota
// by default; the operator tightens it for a larger corpus.
const defaultMinInterval = 500 * time.Millisecond

// ArticleRef is one entry of the starter corpus: the Legifrance article id to
// fetch and an optional human label of the code it belongs to (getArticle does
// not return the code name, so the operator supplies it for display).
type ArticleRef struct {
	ID    string
	Label string
}

// Publisher enqueues a marshaled job at a priority.
type Publisher = evidencesrc.Publisher

// Config configures a Producer. Credentials are the PISTE OAuth2 client
// credentials (an empty pair triggers the graceful skip); TokenURL and APIBaseURL
// override the PISTE endpoints (empty uses production); Articles is the starter
// corpus; ManifestPath persists the per-article fingerprints; MaxPriority is the
// queue's priority ceiling; MaxItems bounds a run (0 = unbounded); MinInterval
// paces requests (0 uses the default); HTTPClient overrides the transport for
// tests.
type Config struct {
	Credentials  Credentials
	TokenURL     string
	APIBaseURL   string
	Articles     []ArticleRef
	ManifestPath string
	MaxPriority  uint8
	MaxItems     int
	MinInterval  time.Duration
	HTTPClient   *http.Client
}

// Producer fetches the configured code articles from the PISTE Legifrance API,
// diffs them against the persisted manifest, and publishes each new or changed
// article as the generic connector.EvidenceJob. It implements crawlnotify.Producer
// so it runs through the shared scheduler and alert wiring like every other
// source. When the PISTE credentials are absent it degrades to a clean skip.
type Producer struct {
	cfg    Config
	client *Client
	pub    Publisher
	logger *slog.Logger
}

// New builds a Producer, failing fast on missing configuration. It does not
// require credentials: a producer built without them still runs and cleanly skips,
// so the source can be wired before it is provisioned.
func New(cfg Config, pub Publisher, logger *slog.Logger) (*Producer, error) {
	if pub == nil {
		return nil, fmt.Errorf("legifrance: publisher is required")
	}
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("legifrance: max priority must be positive, got %d", cfg.MaxPriority)
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = defaultMinInterval
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	tokens := NewTokenSource(cfg.TokenURL, cfg.Credentials, cfg.HTTPClient)
	client := NewClient(cfg.APIBaseURL, tokens, cfg.HTTPClient)
	return &Producer{cfg: cfg, client: client, pub: pub, logger: logger}, nil
}

// Name identifies this producer to the alerting seam and matches the connector
// descriptor Name.
func (p *Producer) Name() string { return Source }

// Scope is the human-readable description of what one run ingests.
func (p *Producer) Scope() string {
	return fmt.Sprintf("Legifrance code articles (%d configured)", len(p.cfg.Articles))
}

// Run fetches each configured article, diffs it against the manifest, and
// publishes the new or changed ones, then persists the manifest. It degrades to a
// clean skip (a finished run publishing nothing) when the PISTE credentials are
// absent or the corpus is empty, so an unprovisioned source never fails the fleet.
// Stats.New counts articles published; Stats.Skipped counts articles left
// unpublished (unchanged, dereferenced-away, or deferred by the backfill bound).
func (p *Producer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	if !p.cfg.Credentials.Present() {
		if p.cfg.Credentials.Partial() {
			// One half set but not the other is almost always a config typo; name it so
			// the operator does not chase a silent "absent" skip.
			p.logger.WarnContext(ctx, "legifrance PISTE credentials are only partially set (client id and secret must both be present); skipping run",
				slog.Bool("has_client_id", p.cfg.Credentials.ClientID != ""),
				slog.Bool("has_client_secret", p.cfg.Credentials.ClientSecret != ""))
		} else {
			p.logger.WarnContext(ctx, "legifrance PISTE credentials absent; skipping run (no articles ingested)")
		}
		return crawlnotify.Stats{}, nil
	}
	if len(p.cfg.Articles) == 0 {
		p.logger.InfoContext(ctx, "legifrance corpus is empty; nothing to ingest")
		return crawlnotify.Stats{}, nil
	}

	prev, err := evidencesrc.LoadManifest(p.cfg.ManifestPath)
	if err != nil {
		return crawlnotify.Stats{}, err
	}
	next := evidencesrc.NewManifest()
	var stats crawlnotify.Stats

	for i, ref := range p.cfg.Articles {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		if i > 0 {
			if err := sleepCtx(ctx, p.cfg.MinInterval); err != nil {
				return stats, err
			}
		}
		if p.cfg.MaxItems > 0 && stats.New >= p.cfg.MaxItems {
			stats.Skipped++
			continue
		}
		art, err := p.client.GetArticle(ctx, ref.ID)
		if err != nil {
			return stats, err
		}
		if art == nil {
			// The API dereferenced the id to nothing (repealed or unknown article):
			// skip it without recording a fingerprint so a later re-consolidation is
			// still picked up.
			p.logger.WarnContext(ctx, "legifrance article dereferenced to nothing, skipping", slog.String("id", ref.ID))
			stats.Skipped++
			continue
		}
		fp := articleFingerprint(*art)
		if !prev.Changed(art.ID, fp) {
			next.Set(art.ID, fp)
			stats.Skipped++
			continue
		}
		if err := p.publishRecord(ctx, buildRecord(*art, ref.Label)); err != nil {
			return stats, err
		}
		next.Set(art.ID, fp)
		stats.New++
	}

	if err := evidencesrc.SaveManifest(p.cfg.ManifestPath, next); err != nil {
		return stats, err
	}
	p.logger.InfoContext(ctx, "legifrance ingest finished",
		slog.Int("published", stats.New), slog.Int("unchanged_or_skipped", stats.Skipped))
	return stats, nil
}

// publishRecord publishes every chunk of a record as a validated evidence job.
func (p *Producer) publishRecord(ctx context.Context, rec evidencesrc.Record) error {
	for _, job := range rec.Jobs {
		if err := job.Validate(); err != nil {
			return fmt.Errorf("legifrance: invalid evidence job for %q: %w", rec.ExternalID, err)
		}
		body, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("legifrance: encode evidence job for %q: %w", rec.ExternalID, err)
		}
		if err := p.pub.Publish(ctx, body, p.cfg.MaxPriority); err != nil {
			return fmt.Errorf("legifrance: publish evidence job for %q: %w", rec.ExternalID, err)
		}
	}
	return nil
}

// sleepCtx sleeps for d or returns early if ctx is canceled, so the quota pacing
// never blocks a shutdown.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ensure Producer satisfies the producer seam at compile time.
var _ crawlnotify.Producer = (*Producer)(nil)
