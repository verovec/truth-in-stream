package postgres

import (
	"context"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/stats"
)

// statEmbedder is a deterministic stand-in for the document embedder: every
// text gets the same fixed unit vector, so a stored stat passage is the nearest
// neighbor of a query embedded the same way. This proves the wiring (ingest ->
// store -> SearchWiki) without a network embedding call; the rendering and
// adapter are unit-tested separately.
type statEmbedder struct{ vec []float32 }

func (e statEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = e.vec
	}
	return out, nil
}

func statDatapoints() []domain.Datapoint {
	base := domain.Datapoint{
		SourceName: "Eurostat",
		SourceURL:  "https://ec.europa.eu/eurostat/api/dissemination/sdmx/2.1/data/MIGR_RESFIRST/A.TOTAL.TOTAL.TOTAL.PER.FR?format=SDMX-CSV",
		Dataset:    "MIGR_RESFIRST",
		SeriesKey:  "A.TOTAL.TOTAL.TOTAL.PER.FR",
		Title:      "Premiers titres de séjour délivrés",
		Geography:  "France",
		Dimensions: []string{"toutes nationalités", "tous motifs"},
		Unit:       "personnes",
	}
	a, b := base, base
	a.Period, a.Figure = "2021", 287179
	b.Period, b.Figure = "2022", 326948
	return []domain.Datapoint{a, b}
}

// TestStatsRunStoresAndRetrievesThroughWikiPath is the card's end-to-end DB
// proof: rendered statistical passages land in the evidence corpus, are
// retrieved through the same SearchWiki path the fact-check verifier uses, and a
// re-run does not duplicate a datapoint+period.
func TestStatsRunStoresAndRetrievesThroughWikiPath(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	emb := statEmbedder{vec: fullEmbedding()}
	dps := statDatapoints()

	n, err := stats.Run(ctx, fixedSource{dps}, emb, store, 0)
	if err != nil {
		t.Fatalf("stats.Run: %v", err)
	}
	if n != 2 {
		t.Fatalf("stats.Run wrote %d, want 2", n)
	}

	// Retrieval through the existing evidence path returns the rendered
	// passages, nearest first (same fixed vector -> distance 0).
	hits, err := store.SearchWiki(ctx, fullEmbedding(), 5)
	if err != nil {
		t.Fatalf("SearchWiki: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("SearchWiki returned %d hits, want >= 2", len(hits))
	}
	var found2022 bool
	for _, h := range hits {
		// A retrieved passage carries figure, period, geography, and a
		// resolvable source URL.
		if h.URL == "" {
			t.Errorf("hit missing source url: %+v", h)
		}
		if h.Content == "" || h.Title == "" {
			t.Errorf("hit missing content/title: %+v", h)
		}
		if h.Content != "" && containsAllStrings(h.Content, "France", "personnes") {
			if containsAllStrings(h.Content, "2022", "326 948") {
				found2022 = true
			}
		}
		// score = 1 - distance must clear the evidence floor (0.6).
		if score := 1 - float64(h.Distance); score < 0.6 {
			t.Errorf("hit score %.3f below evidence floor 0.6", score)
		}
	}
	if !found2022 {
		t.Errorf("did not retrieve the 2022 France permits passage; hits=%v", hits)
	}

	// Re-run identical data: idempotent, no duplicate rows for the same
	// datapoint+period.
	if _, err := stats.Run(ctx, fixedSource{dps}, emb, store, 0); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var count int64
	if err := store.pool.QueryRow(
		ctx,
		"SELECT count(DISTINCT page_id) FROM wiki_chunks WHERE corpus = $1", stats.StatCorpus,
	).Scan(&count); err != nil {
		t.Fatalf("count stat pages: %v", err)
	}
	if count != 1 {
		t.Errorf("after re-run distinct stat pages = %d, want 1 (one series, no duplicates)", count)
	}
	rerunHits, err := store.SearchWiki(ctx, fullEmbedding(), 10)
	if err != nil {
		t.Fatalf("SearchWiki after re-run: %v", err)
	}
	if len(rerunHits) != 2 {
		t.Errorf("after re-run hits = %d, want 2 (no duplicate passages)", len(rerunHits))
	}
}

// TestStatsExcludedFromWikiMaintenanceReads proves statistical rows, though
// retrievable through SearchWiki, are excluded from the encyclopedic-corpus
// maintenance reads: the delta-sync page-count denominator (CountPages) and the
// clustering scan (EmbeddedChunks). Mixing them in would skew the wiki
// bulk-recommendation guard and cluster statistics into encyclopedic topics.
func TestStatsExcludedFromWikiMaintenanceReads(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// One genuine Wikipedia chunk and the two statistical passages.
	wiki := domain.WikiChunk{
		PageID: 42, ChunkIndex: 0, Title: "Immigration", URL: "https://w/imm",
		RevisionID: 1, Corpus: "simplewiki", Content: "Immigration is the movement of people.",
		Kind: domain.WikiChunkKindLead, Embedding: fullEmbedding(),
	}
	if err := store.UpsertEmbeddedChunk(ctx, wiki); err != nil {
		t.Fatalf("seed wiki chunk: %v", err)
	}
	if _, err := stats.Run(ctx, fixedSource{statDatapoints()}, statEmbedder{vec: fullEmbedding()}, store, 0); err != nil {
		t.Fatalf("stats.Run: %v", err)
	}

	// CountPages counts the wiki page only, not the statistical series.
	pages, err := store.CountPages(ctx)
	if err != nil {
		t.Fatalf("CountPages: %v", err)
	}
	if pages != 1 {
		t.Errorf("CountPages = %d, want 1 (statistical series excluded)", pages)
	}

	// The clustering scan returns the wiki chunk only.
	embedded, err := store.EmbeddedChunks(ctx, domain.WikiCursor{}, 100)
	if err != nil {
		t.Fatalf("EmbeddedChunks: %v", err)
	}
	if len(embedded) != 1 {
		t.Fatalf("EmbeddedChunks returned %d, want 1 (statistics excluded)", len(embedded))
	}
	if embedded[0].PageID != 42 {
		t.Errorf("clustering scan returned page %d, want the wiki page 42", embedded[0].PageID)
	}

	// SearchWiki still retrieves the statistics (the feature is intact).
	hits, err := store.SearchWiki(ctx, fullEmbedding(), 10)
	if err != nil {
		t.Fatalf("SearchWiki: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("SearchWiki returned %d, want 3 (1 wiki + 2 stats both retrievable)", len(hits))
	}
}

// nationalDatapoints are one interior-ministry and one INSEE datapoint, each
// under its own statistical corpus, so a test can prove the national corpora
// added in VER-120 are excluded from the wiki-only maintenance reads exactly
// like the EU corpus.
func nationalDatapoints() []domain.Datapoint {
	interior := domain.Datapoint{
		SourceName: "Ministère de l'Intérieur",
		SourceURL:  "https://www.data.gouv.fr/api/1/datasets/r/c2cd00ad-b43f-4bee-87dd-8c52991e4dc8",
		Dataset:    "titres-de-sejour-2023",
		SeriesKey:  "flux/pays/MAR",
		Title:      "Premiers titres de séjour délivrés",
		Geography:  "France",
		Dimensions: []string{"Maroc"},
		Period:     "2023",
		Figure:     33268,
		Unit:       "personnes",
	}
	insee := domain.Datapoint{
		SourceName: "Insee",
		SourceURL:  "https://bdm.insee.fr/series/sdmx/data/SERIES_BDM/001",
		Dataset:    "EEC",
		SeriesKey:  "001",
		Title:      "Taux d'emploi",
		Geography:  "France",
		Dimensions: []string{"immigrés", "15 à 64 ans"},
		Period:     "2023",
		Figure:     59.8,
		Unit:       "%",
	}
	return []domain.Datapoint{interior, insee}
}

// TestNationalStatsExcludedFromWikiMaintenanceReads proves the national
// statistical corpora (interior ministry, INSEE) introduced in VER-120 are
// excluded from CountPages and the clustering scan just like the EU corpus,
// while still being retrievable through SearchWiki.
func TestNationalStatsExcludedFromWikiMaintenanceReads(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	wiki := domain.WikiChunk{
		PageID: 42, ChunkIndex: 0, Title: "Immigration", URL: "https://w/imm",
		RevisionID: 1, Corpus: "simplewiki", Content: "Immigration is the movement of people.",
		Kind: domain.WikiChunkKindLead, Embedding: fullEmbedding(),
	}
	if err := store.UpsertEmbeddedChunk(ctx, wiki); err != nil {
		t.Fatalf("seed wiki chunk: %v", err)
	}

	emb := statEmbedder{vec: fullEmbedding()}
	if _, err := stats.Run(ctx, statSource{corpus: domain.InteriorStatCorpus, dps: nationalDatapoints()[:1]}, emb, store, 0); err != nil {
		t.Fatalf("interior run: %v", err)
	}
	if _, err := stats.Run(ctx, statSource{corpus: domain.INSEEStatCorpus, dps: nationalDatapoints()[1:]}, emb, store, 0); err != nil {
		t.Fatalf("insee run: %v", err)
	}

	pages, err := store.CountPages(ctx)
	if err != nil {
		t.Fatalf("CountPages: %v", err)
	}
	if pages != 1 {
		t.Errorf("CountPages = %d, want 1 (both national statistical corpora excluded)", pages)
	}

	embedded, err := store.EmbeddedChunks(ctx, domain.WikiCursor{}, 100)
	if err != nil {
		t.Fatalf("EmbeddedChunks: %v", err)
	}
	if len(embedded) != 1 {
		t.Fatalf("EmbeddedChunks returned %d, want 1 (national statistics excluded)", len(embedded))
	}
	if embedded[0].PageID != 42 {
		t.Errorf("clustering scan returned page %d, want the wiki page 42", embedded[0].PageID)
	}

	hits, err := store.SearchWiki(ctx, fullEmbedding(), 10)
	if err != nil {
		t.Fatalf("SearchWiki: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("SearchWiki returned %d, want 3 (1 wiki + 2 national stats retrievable)", len(hits))
	}
}

type fixedSource struct{ dps []domain.Datapoint }

func (f fixedSource) Datapoints(context.Context) ([]domain.Datapoint, error) {
	return f.dps, nil
}

func (fixedSource) Corpus() string { return domain.StatCorpus }

// statSource yields fixed datapoints under a chosen statistical corpus, so the
// national-corpus exclusion test can ingest under the interior and INSEE labels.
type statSource struct {
	corpus string
	dps    []domain.Datapoint
}

func (s statSource) Datapoints(context.Context) ([]domain.Datapoint, error) {
	return s.dps, nil
}
func (s statSource) Corpus() string { return s.corpus }

func containsAllStrings(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
