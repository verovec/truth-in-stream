package seed

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TVChannelStore upserts channels idempotently, keyed by slug so reseeding keeps
// a stable id and never re-arms an operator's toggle. The postgres store
// satisfies it.
type TVChannelStore interface {
	UpsertTVChannelBySlug(ctx context.Context, c domain.TVChannel) (domain.TVChannel, error)
}

// TVChannels returns the seed registry: the free, non-DRM sources the design
// enumerates (official 24/7 YouTube lives and the parliamentary HLS portals),
// every channel disabled so the operator turns each on deliberately, archiving
// armed so an enabled channel records unless the operator opts out. source_ref
// is the stable channel URL, never a resolved manifest; the capture worker
// re-resolves it on each start. The list is the single source of truth for the
// seeded registry.
func TVChannels() []domain.TVChannel {
	return []domain.TVChannel{
		{Slug: "franceinfo", Name: "franceinfo", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/franceinfo/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "france24-fr", Name: "France 24 (FR)", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@FRANCE24/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "bfmtv", Name: "BFMTV", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@BFMTV/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "euronews-fr", Name: "Euronews (FR)", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/c/euronewsfr/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "lcp", Name: "LCP", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@LCP/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "public-senat", Name: "Public Senat", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@publicsenat/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "cnews", Name: "CNEWS", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@CNEWSofficiel/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "lci", Name: "LCI", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@LCI/live", Enabled: false, ArchiveEnabled: true},
		{Slug: "assemblee-nationale", Name: "Assemblee nationale", SourceKind: domain.TVSourceHLS, SourceRef: "https://videos.assemblee-nationale.fr/direct", Enabled: false, ArchiveEnabled: true},
		{Slug: "senat", Name: "Senat", SourceKind: domain.TVSourceHLS, SourceRef: "https://videos.senat.fr/direct", Enabled: false, ArchiveEnabled: true},
	}
}

// InsertTVChannels upserts every channel in the registry, idempotently, and
// returns the number seeded. Reseeding refreshes the descriptive fields but
// preserves each channel's operator-controlled enabled/archive_enabled state.
func InsertTVChannels(ctx context.Context, store TVChannelStore) (int, error) {
	channels := TVChannels()
	for _, c := range channels {
		if _, err := store.UpsertTVChannelBySlug(ctx, c); err != nil {
			return 0, fmt.Errorf("seed: upsert tv channel %q: %w", c.Slug, err)
		}
	}
	return len(channels), nil
}
