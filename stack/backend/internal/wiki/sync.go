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

// Store is the slice of the wiki store the bulk run needs.
type Store interface {
	// EnsureCorpus claims the store for one corpus; it fails when the store
	// already holds a different one. wiki_chunks keys rows by (page, chunk
	// index) only, so page ids from two corpora would silently collide.
	EnsureCorpus(ctx context.Context, corpus string) error
	// UpsertChunks inserts or replaces chunks by (page, chunk index).
	UpsertChunks(ctx context.Context, chunks []domain.WikiChunk) error
	// TrimPages removes the stale tail of each page's chunks.
	TrimPages(ctx context.Context, trims []domain.WikiTrim) error
	// SetSyncState records the corpus checkpoint after a successful run.
	SetSyncState(ctx context.Context, st domain.WikiSyncState) error
}

// Stats summarizes a bulk run.
type Stats struct {
	PagesSeen    int
	PagesSkipped int
	PagesStored  int
	Chunks       int
}

// RunBulk ingests a downloaded multistream dump: streams decompress in
// parallel, each page's lead section is extracted, stripped, and chunked, and
// chunks land in the store. Redirects, non-article namespaces, disambiguation
// pages, and pages with empty leads are skipped - and their chunks from any
// earlier run are removed, as is the stale tail of pages whose lead shrank,
// so re-running against a newer dump converges instead of accreting. On
// success the corpus sync state is checkpointed at the dump's publication
// time.
func RunBulk(ctx context.Context, store Store, files DumpFiles, corpus string) (Stats, error) {
	if err := store.EnsureCorpus(ctx, corpus); err != nil {
		return Stats{}, fmt.Errorf("wiki: claim corpus %q: %w", corpus, err)
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
		return Stats{}, fmt.Errorf("wiki: dump for %q yielded no pages; refusing to checkpoint", corpus)
	}

	state := domain.WikiSyncState{
		Corpus:       corpus,
		LastChangeTS: parseDumpTime(files.Version),
		DumpVersion:  files.Version,
	}
	if err := store.SetSyncState(ctx, state); err != nil {
		return Stats{}, fmt.Errorf("wiki: checkpoint sync state: %w", err)
	}
	return stats, nil
}

// storePages chunks one stream's worth of pages, trims every page's stale
// tail, and upserts the new chunks, each as a single batch.
func storePages(ctx context.Context, store Store, pages []Page, corpus string) (Stats, error) {
	var (
		stats  Stats
		chunks []domain.WikiChunk
	)
	trims := make([]domain.WikiTrim, 0, len(pages))
	for _, p := range pages {
		stats.PagesSeen++
		lead := ""
		if keepPage(p) {
			lead = ExtractLead(p.Text)
		}
		pieces := Chunk(p.Title, lead)
		trims = append(trims, domain.WikiTrim{PageID: p.ID, FromIndex: len(pieces)})
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
			})
		}
	}
	stats.Chunks = len(chunks)
	if len(trims) > 0 {
		if err := store.TrimPages(ctx, trims); err != nil {
			return Stats{}, fmt.Errorf("wiki: trim stale chunks: %w", err)
		}
	}
	if len(chunks) > 0 {
		if err := store.UpsertChunks(ctx, chunks); err != nil {
			return Stats{}, fmt.Errorf("wiki: upsert chunks: %w", err)
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
