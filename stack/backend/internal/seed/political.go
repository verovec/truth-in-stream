package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// PoliticalClaimWriter is the slice of the curated political claim store the
// political seed needs: upsert one fully embedded two-axis claim. The write is
// idempotent (keyed by id), so reseeding rewrites the same row rather than
// duplicating it. *postgres.Store satisfies it via UpsertPoliticalClaim.
type PoliticalClaimWriter interface {
	UpsertPoliticalClaim(ctx context.Context, claim domain.PoliticalClaim) error
}

// politicalClaimFile is one curated two-axis political claim as written in the
// seed fixture: the claim text as typically stated, its literal verdict and any
// manipulation flags, and the real source provenance. CheckedAt is optional
// (RFC3339); the seed claims are hand-curated rather than dated outlet checks, so
// it is normally omitted and stored as SQL NULL. The shape mirrors the
// factcheckjob.ClaimJob the live crawler writes, so the seed and the crawler feed
// the same political_claims schema.
type politicalClaimFile struct {
	ID             string   `json:"id"`
	Text           string   `json:"text"`
	LiteralVerdict string   `json:"literal_verdict"`
	Flags          []string `json:"flags,omitempty"`
	SourceName     string   `json:"source_name"`
	SourceURL      string   `json:"source_url"`
	QuotedSpan     string   `json:"quoted_span,omitempty"`
	Outlet         string   `json:"outlet"`
	CheckedAt      string   `json:"checked_at,omitempty"`
}

// LoadPoliticalClaims decodes and validates the curated immigration talking-point
// fixture: a JSON array of two-axis political claims with unique ids. Each entry
// is validated against the political_claims column constraints (literal verdict
// and every manipulation flag against the domain enums) so a malformed fixture
// fails the load rather than reaching the store. Embeddings are filled at seed
// time, not carried here.
func LoadPoliticalClaims(r io.Reader) ([]domain.PoliticalClaim, error) {
	var files []politicalClaimFile
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&files); err != nil {
		return nil, fmt.Errorf("seed: decode political claims: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("seed: political claims: fixture is empty")
	}

	seen := make(map[string]struct{}, len(files))
	claims := make([]domain.PoliticalClaim, len(files))
	for i, f := range files {
		switch {
		case f.ID == "":
			return nil, fmt.Errorf("seed: political claim %d: empty id", i)
		case f.Text == "":
			return nil, fmt.Errorf("seed: political claim %q: empty text", f.ID)
		case !domain.LiteralVerdict(f.LiteralVerdict).Valid():
			return nil, fmt.Errorf("seed: political claim %q: invalid literal verdict %q", f.ID, f.LiteralVerdict)
		case f.SourceName == "":
			return nil, fmt.Errorf("seed: political claim %q: empty source name", f.ID)
		case f.SourceURL == "":
			return nil, fmt.Errorf("seed: political claim %q: empty source url", f.ID)
		case f.Outlet == "":
			return nil, fmt.Errorf("seed: political claim %q: empty outlet", f.ID)
		}
		if _, dup := seen[f.ID]; dup {
			return nil, fmt.Errorf("seed: political claim %q: duplicate id", f.ID)
		}
		seen[f.ID] = struct{}{}

		flags := make([]domain.ManipulationFlag, len(f.Flags))
		for j, raw := range f.Flags {
			flag := domain.ManipulationFlag(raw)
			if !flag.Valid() {
				return nil, fmt.Errorf("seed: political claim %q: invalid manipulation flag %q", f.ID, raw)
			}
			flags[j] = flag
		}

		checkedAt, err := parseCheckedAt(f.CheckedAt)
		if err != nil {
			return nil, fmt.Errorf("seed: political claim %q: %w", f.ID, err)
		}

		claims[i] = domain.PoliticalClaim{
			ID:             f.ID,
			Text:           f.Text,
			LiteralVerdict: domain.LiteralVerdict(f.LiteralVerdict),
			Flags:          flags,
			SourceName:     f.SourceName,
			SourceURL:      f.SourceURL,
			QuotedSpan:     f.QuotedSpan,
			Outlet:         f.Outlet,
			CheckedAt:      checkedAt,
		}
	}
	return claims, nil
}

// parseCheckedAt parses the optional RFC3339 publication timestamp. An empty
// string is a valid "no date recorded" and yields the zero time, which the store
// maps to SQL NULL.
func parseCheckedAt(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse checked-at %q: %w", raw, err)
	}
	return t, nil
}

// InsertPoliticalClaims embeds each claim's text through embedder and upserts the
// fully embedded curated claim into the political claim DB. It is idempotent:
// claims upsert by id, so reseeding rewrites the same rows and never disturbs
// curated claims (such as the live-crawled ones) under other ids.
func InsertPoliticalClaims(ctx context.Context, store PoliticalClaimWriter, embedder Embedder, claims []domain.PoliticalClaim) error {
	if len(claims) == 0 {
		return nil
	}

	texts := make([]string, len(claims))
	for i, c := range claims {
		texts[i] = c.Text
	}
	embeddings, err := embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		return fmt.Errorf("seed: political claims: embed: %w", err)
	}
	if len(embeddings) != len(claims) {
		return fmt.Errorf("seed: political claims: got %d embeddings, want %d", len(embeddings), len(claims))
	}

	for i, c := range claims {
		c.Embedding = embeddings[i]
		if err := store.UpsertPoliticalClaim(ctx, c); err != nil {
			return fmt.Errorf("seed: political claims: upsert %q: %w", c.ID, err)
		}
	}
	return nil
}
