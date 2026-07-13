// Package claimskg is the DB-free one-time ClaimsKG seed producer. ClaimsKG is a
// large (2023-vintage) knowledge graph of fact-checked claims aggregated from many
// international fact-checkers; this producer reads a CSV/TSV export of it (the
// operator exports it from the ClaimsKG portal or a mirror) and publishes one
// self-contained curated-claim job per row to the fact-check queue for the existing
// worker to embed and upsert into political_claims.
//
// It is a deliberate one-shot behind an explicit flag: it never runs on a schedule
// and is a no-op unless both Enabled is set and a seed file is provided, so a large
// stale snapshot is only ingested on a considered operator action. Every record is
// marked with its provenance (source_name carries "ClaimsKG" and the vintage) so a
// borrowed verdict is attributable and its age is visible. Dedup with the live paths
// is by review URL, the stable claim ID, so a claim ClaimsKG and the Google API both
// carry collapses to one row.
package claimskg

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/claimnorm"
	"github.com/verovec/truth-in-stream/backend/internal/claimrating"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
)

// Publisher enqueues a marshaled claim job at a priority; the cmd-layer broker
// adapter satisfies it.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config configures a Client. Enabled and SeedFile together gate the run: without
// both, Run is a no-op. Vintage marks the snapshot's age in each record's
// provenance; MaxPriority is the queue ceiling; Delimiter overrides the CSV
// delimiter (0 = comma; use '\t' for a TSV export).
type Config struct {
	Enabled     bool
	Vintage     string
	MaxPriority uint8
	Delimiter   rune
}

// Client reads a ClaimsKG export and publishes curated-claim jobs.
type Client struct {
	enabled     bool
	vintage     string
	maxPriority uint8
	delimiter   rune
}

// New builds a Client, failing fast on a missing priority.
func New(cfg Config) (*Client, error) {
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("claimskg: max priority must be positive, got %d", cfg.MaxPriority)
	}
	vintage := cfg.Vintage
	if vintage == "" {
		vintage = "2023"
	}
	delim := cfg.Delimiter
	if delim == 0 {
		delim = ','
	}
	return &Client{enabled: cfg.Enabled, vintage: vintage, maxPriority: cfg.MaxPriority, delimiter: delim}, nil
}

// Stats summarizes a seed run.
type Stats struct {
	Published    int
	Unverifiable int
	Skipped      int
}

// Enabled reports whether the seed is armed. A disabled client never reads the file.
func (c *Client) Enabled() bool { return c.enabled }

// column aliases: ClaimsKG exports vary between the portal CSV and the mirrors, so
// each logical field accepts several header spellings (matched case-insensitively).
var (
	claimCols  = []string{"claimreviewed", "claim", "text", "claim_text"}
	ratingCols = []string{"alternatename", "rating", "ratingname", "truthrating", "reviewrating_alternatename", "rating_name"}
	urlCols    = []string{"claimreview_url", "url", "link", "review_url"}
	authorCols = []string{"claimreview_author_name", "author", "source", "organization", "fact_checker"}
	dateCols   = []string{"claimreview_datepublished", "date", "datepublished", "review_date"}
)

// Run reads the CSV/TSV export from r and publishes a curated-claim job per row. It
// is a no-op (zero stats, nil error) when the seed is disabled. A row missing claim
// text or a review URL is skipped; a row whose rating does not map is published as
// unverifiable. A nil logger falls back to slog.Default.
func (c *Client) Run(ctx context.Context, logger *slog.Logger, pub Publisher, r io.Reader) (Stats, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if !c.enabled {
		logger.InfoContext(ctx, "claimskg seed disabled, skipping (set the enable flag and a seed file to run)")
		return Stats{}, nil
	}

	cr := csv.NewReader(r)
	cr.Comma = c.delimiter
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	header, err := cr.Read()
	if err != nil {
		return Stats{}, fmt.Errorf("claimskg: read header: %w", err)
	}
	idx := indexColumns(header)
	// col returns the column index for the first matching alias, or -1 when none is
	// present. A missing OPTIONAL column MUST yield -1 (field() then reads ""), never
	// a default index 0 — otherwise rating/author/date would silently read the claim
	// text in column 0.
	col := func(aliases []string) int {
		for _, a := range aliases {
			if i, ok := idx[a]; ok {
				return i
			}
		}
		return -1
	}
	claimI := col(claimCols)
	urlI := col(urlCols)
	if claimI < 0 || urlI < 0 {
		return Stats{}, fmt.Errorf("claimskg: export is missing a claim-text or review-url column (have %v)", header)
	}
	ratingI := col(ratingCols)
	authorI := col(authorCols)
	dateI := col(dateCols)

	var stats Stats
	for {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("claimskg: read row: %w", err)
		}
		job, unverifiable, ok := c.toJob(row, claimI, urlI, ratingI, authorI, dateI)
		if !ok {
			stats.Skipped++
			continue
		}
		body, err := json.Marshal(job)
		if err != nil {
			return stats, fmt.Errorf("claimskg: encode job %q: %w", job.ID, err)
		}
		if err := pub.Publish(ctx, body, c.maxPriority); err != nil {
			return stats, fmt.Errorf("claimskg: publish job %q: %w", job.ID, err)
		}
		stats.Published++
		if unverifiable {
			stats.Unverifiable++
		}
	}
	logger.InfoContext(ctx, "claimskg seed finished",
		slog.String("vintage", c.vintage),
		slog.Int("published", stats.Published),
		slog.Int("unverifiable", stats.Unverifiable),
		slog.Int("skipped", stats.Skipped))
	return stats, nil
}

// toJob builds a self-contained job from one export row, returning ok=false when it
// lacks claim text or a review URL. The bool reports whether the rating fell back to
// unverifiable. Provenance (source_name) carries "ClaimsKG" and the vintage so a
// borrowed verdict is attributable and its age visible.
func (c *Client) toJob(row []string, claimI, urlI, ratingI, authorI, dateI int) (factcheckjob.ClaimJob, bool, bool) {
	claim := field(row, claimI)
	rawURL := field(row, urlI)
	if claim == "" || rawURL == "" {
		return factcheckjob.ClaimJob{}, false, false
	}
	// The canonical review URL is the cross-path dedup key.
	reviewURL := claimnorm.CanonicalURL(rawURL)
	verdict, mapped := claimrating.Normalize(field(row, ratingI), claimrating.NumericRating{})
	outlet := hostOf(reviewURL)
	if outlet == "" {
		outlet = field(row, authorI)
	}
	if outlet == "" {
		outlet = "claimskg"
	}
	return factcheckjob.ClaimJob{
		ID:             reviewURL,
		Text:           claim,
		LiteralVerdict: string(verdict),
		SourceName:     "ClaimsKG (" + c.vintage + " snapshot" + sourceSuffix(field(row, authorI)) + ")",
		SourceURL:      reviewURL,
		QuotedSpan:     claim,
		Outlet:         outlet,
		CheckedAt:      normalizeDate(field(row, dateI)),
	}, !mapped, true
}

func sourceSuffix(author string) string {
	if author == "" {
		return ""
	}
	return ", via " + author
}

func field(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func indexColumns(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return idx
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func normalizeDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}
