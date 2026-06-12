package wiki

import (
	"compress/bzip2"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Store is the slice of the wiki store the bulk ingest needs. Ingest builds the
// next corpus in the staging table, never touching live wiki_chunks, which keeps
// serving the current corpus until the embed run swaps staging over it.
type Store interface {
	// EnsureCorpus claims the store for one corpus; it fails when the store
	// already holds a different one. wiki_chunks keys rows by (page, chunk
	// index) only, so page ids from two corpora would silently collide.
	EnsureCorpus(ctx context.Context, corpus string) error
	// ResetStaging drops any surviving staging table and creates a fresh one
	// stamped building:version, the empty target this ingest fills.
	ResetStaging(ctx context.Context, version string) error
	// UpsertStagingChunks inserts chunks (NULL embedding) into staging.
	UpsertStagingChunks(ctx context.Context, chunks []domain.WikiChunk) error
	// CarryForwardEmbeddings copies unchanged chunks' embeddings from live into
	// staging by content match, so only changed and new chunks are re-embedded.
	CarryForwardEmbeddings(ctx context.Context) (int64, error)
	// MarkStagingReady stamps the materialized staging ready:version, a resume
	// target for the embed run.
	MarkStagingReady(ctx context.Context, version string) error
}

// Stats summarizes a bulk ingest.
type Stats struct {
	PagesSeen    int
	PagesSkipped int
	PagesStored  int
	Chunks       int
	Carried      int64
}

// RunBulk ingests a downloaded multistream dump into the staging table: streams
// decompress in parallel, each page's lead section is extracted, stripped, and
// chunked, and the chunks are inserted into a freshly reset staging table.
// Redirects, non-article namespaces, disambiguation pages, and pages with empty
// leads are skipped; because staging is built solely from the current dump,
// pages that left the corpus simply never enter it - no orphans accrue. Once
// every page is staged, unchanged chunks carry their embeddings forward from
// live (so only changed and new chunks are re-embedded) and staging is stamped
// ready for the embed run. Live wiki_chunks is untouched until the swap.
func RunBulk(ctx context.Context, store Store, files DumpFiles, corpus string) (Stats, error) {
	if err := store.EnsureCorpus(ctx, corpus); err != nil {
		return Stats{}, fmt.Errorf("wiki: claim corpus %q: %w", corpus, err)
	}
	if err := store.ResetStaging(ctx, files.Version); err != nil {
		return Stats{}, fmt.Errorf("wiki: reset staging: %w", err)
	}

	ranges, err := loadIndex(files.IndexPath)
	if err != nil {
		return Stats{}, err
	}

	dump, err := os.Open(files.DumpPath)
	if err != nil {
		return Stats{}, fmt.Errorf("wiki: open dump: %w", err)
	}
	defer func() { _ = dump.Close() }()
	info, err := dump.Stat()
	if err != nil {
		return Stats{}, fmt.Errorf("wiki: stat dump: %w", err)
	}

	var (
		mu    sync.Mutex
		stats Stats
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for _, sr := range ranges {
		g.Go(func() error {
			pages, err := ParsePages(DecompressStream(dump, sr, info.Size()))
			if err != nil {
				return fmt.Errorf("wiki: stream at %d: %w", sr.Start, err)
			}
			st, err := storePages(gctx, store, pages, corpus)
			if err != nil {
				return err
			}
			mu.Lock()
			stats.PagesSeen += st.PagesSeen
			stats.PagesSkipped += st.PagesSkipped
			stats.PagesStored += st.PagesStored
			stats.Chunks += st.Chunks
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return Stats{}, err
	}
	if stats.PagesSeen == 0 {
		return Stats{}, fmt.Errorf("wiki: dump for %q yielded no pages; refusing to stage", corpus)
	}

	carried, err := store.CarryForwardEmbeddings(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("wiki: carry forward embeddings: %w", err)
	}
	stats.Carried = carried
	if err := store.MarkStagingReady(ctx, files.Version); err != nil {
		return Stats{}, fmt.Errorf("wiki: mark staging ready: %w", err)
	}
	return stats, nil
}

// storePages chunks one stream's worth of pages and inserts the new chunks into
// staging as a single batch. No trim is needed: staging is reset empty before
// the run, so a page contributes exactly the chunks of the current dump.
func storePages(ctx context.Context, store Store, pages []Page, corpus string) (Stats, error) {
	var (
		stats  Stats
		chunks []domain.WikiChunk
	)
	for _, p := range pages {
		stats.PagesSeen++
		lead := ""
		if keepPage(p) {
			lead = ExtractLead(p.Text)
		}
		pieces := Chunk(p.Title, lead)
		if len(pieces) == 0 {
			stats.PagesSkipped++
			continue
		}
		stats.PagesStored++
		for i, content := range pieces {
			chunks = append(chunks, domain.WikiChunk{
				PageID:     p.ID,
				ChunkIndex: i,
				Title:      p.Title,
				URL:        pageURL(corpus, p.Title),
				RevisionID: p.RevisionID,
				Corpus:     corpus,
				Content:    content,
				Kind:       domain.WikiChunkKindLead,
			})
		}
	}
	stats.Chunks = len(chunks)
	if len(chunks) > 0 {
		if err := store.UpsertStagingChunks(ctx, chunks); err != nil {
			return Stats{}, fmt.Errorf("wiki: stage chunks: %w", err)
		}
	}
	return stats, nil
}

// keepPage reports whether a page is an article worth ingesting: mainspace,
// not a redirect, not a disambiguation page.
func keepPage(p Page) bool {
	return p.NS == 0 && !p.Redirect && !IsDisambiguation(p.Text)
}

// loadIndex reads and parses the bz2-compressed multistream index.
func loadIndex(path string) ([]StreamRange, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("wiki: open index: %w", err)
	}
	defer func() { _ = f.Close() }()

	ranges, err := ParseIndex(bzip2.NewReader(f))
	if err != nil {
		return nil, err
	}
	return ranges, nil
}

// pageURL builds the canonical article URL for a corpus page. Corpus names
// are restricted to Wikipedia dumps ("<lang>wiki", validated at config load),
// and the article path uses underscores for spaces.
func pageURL(corpus, title string) string {
	lang := strings.TrimSuffix(corpus, "wiki")
	path := url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	return fmt.Sprintf("https://%s.wikipedia.org/wiki/%s", lang, path)
}

// parseDumpTime turns the dump's Last-Modified value into the corpus
// checkpoint time; a missing or malformed header yields the zero time (a
// NULL checkpoint, which the delta sync treats as "re-bulk required").
func parseDumpTime(version string) time.Time {
	t, err := http.ParseTime(version)
	if err != nil {
		return time.Time{}
	}
	return t
}
