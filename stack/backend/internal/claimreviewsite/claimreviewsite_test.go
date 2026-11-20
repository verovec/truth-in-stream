package claimreviewsite

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

type recordingPublisher struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (p *recordingPublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies = append(p.bodies, body)
	return nil
}

func (p *recordingPublisher) jobs(t *testing.T) []factcheckjob.ClaimJob {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]factcheckjob.ClaimJob, 0, len(p.bodies))
	for _, b := range p.bodies {
		var j factcheckjob.ClaimJob
		if err := json.Unmarshal(b, &j); err != nil {
			t.Fatalf("decode job: %v", err)
		}
		out = append(out, j)
	}
	return out
}

const bodyProse = "ARTICLE-BODY-PROSE-THAT-MUST-NEVER-BE-STORED lorem ipsum dolor."

// standaloneArticle renders a page with a standalone ClaimReview JSON-LD node whose
// url is the page's own absolute URL, plus real article prose the reader must ignore.
func standaloneArticle(pageURL, claim, rating, date, extra string) string {
	return fmt.Sprintf(`<!doctype html><html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@type":"ClaimReview",
 "claimReviewed":%q,"datePublished":%q,"url":%q,
 "author":{"@type":"Organization","name":"Outlet","url":"https://outlet.example/"},
 "reviewRating":{"@type":"Rating","alternateName":%q,"ratingValue":"1","bestRating":"5","worstRating":"1"}%s}
</script></head><body><article><p>%s</p></article></body></html>`,
		claim, date, pageURL, rating, extra, bodyProse)
}

// graphArticle embeds the ClaimReview inside an @graph array alongside other nodes.
func graphArticle(pageURL, claim string) string {
	return fmt.Sprintf(`<html><head><script type="application/ld+json">
{"@context":"https://schema.org","@graph":[
 {"@type":"WebPage","name":"ignored"},
 {"@type":["ClaimReview"],"claimReviewed":%q,"datePublished":"2024-03-03","url":%q,
  "author":{"@type":"Organization","name":"Outlet"},
  "reviewRating":{"@type":"Rating","alternateName":"Vrai"}}]}
</script></head><body>%s</body></html>`, claim, pageURL, bodyProse)
}

type article struct {
	path  string
	build func(pageURL string) string
}

// outletServer serves robots.txt, a sitemap listing every article path plus a
// /private/secret path, and each article page. privateHits counts fetches of the
// /private/secret page so a test can prove a robots Disallow keeps it unfetched.
func outletServer(t *testing.T, robotsBody string, privateHits *int32, articles ...article) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(robotsBody))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		for _, a := range articles {
			fmt.Fprintf(&b, "<url><loc>%s%s</loc></url>", srv.URL, a.path)
		}
		fmt.Fprintf(&b, "<url><loc>%s/private/secret</loc></url>", srv.URL)
		b.WriteString(`</urlset>`)
		_, _ = w.Write([]byte(b.String()))
	})
	for _, a := range articles {
		aa := a
		mux.HandleFunc(aa.path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(aa.build(srv.URL + aa.path)))
		})
	}
	mux.HandleFunc("/private/secret", func(w http.ResponseWriter, _ *http.Request) {
		if privateHits != nil {
			atomic.AddInt32(privateHits, 1)
		}
		// A benign page with no ClaimReview, so a fetch (when robots allows it) is
		// harmless; the counter proves whether robots kept it unfetched.
		_, _ = w.Write([]byte("<html><body>private</body></html>"))
	})
	return srv
}

func hostOf(raw string) string { return strings.TrimPrefix(raw, "http://") }

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	cfg.MaxPriority = 9
	if cfg.MinDelay == 0 {
		cfg.MinDelay = time.Millisecond
	}
	cfg.HTTPClient = &http.Client{}
	cfg.Retry = httpx.RetryConfig{MaxRetries: -1}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRunIngestsThreeOutletsClaimFieldsOnlyNeverBody(t *testing.T) {
	t.Parallel()
	robots := "User-agent: *\nDisallow: /private/\n"

	afp := outletServer(t, robots, nil, article{"/a1", func(u string) string {
		return standaloneArticle(u, "La criminalité a augmenté de 50%", "Faux", "2024-01-01", "")
	}})
	lemonde := outletServer(t, robots, nil, article{"/l1", func(u string) string {
		return graphArticle(u, "Le maire est élu sans les habitants")
	}})
	finfo := outletServer(t, robots, nil, article{"/f1", func(u string) string {
		return standaloneArticle(u, "La citation est authentique", "Plutôt vrai", "2024-02-02", "")
	}})

	outlets := []Outlet{
		{Name: "AFP Factuel", Host: hostOf(afp.URL), Sitemap: afp.URL + "/sitemap.xml"},
		{Name: "Les Décodeurs", Host: hostOf(lemonde.URL), Sitemap: lemonde.URL + "/sitemap.xml"},
		{Name: "franceinfo", Host: hostOf(finfo.URL), Sitemap: finfo.URL + "/sitemap.xml"},
	}
	c := newTestClient(t, Config{Outlets: outlets})
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 3 {
		t.Fatalf("published = %d, want 3 (one per outlet)", stats.Published)
	}
	jobs := pub.jobs(t)
	byOutlet := map[string]factcheckjob.ClaimJob{}
	for _, j := range jobs {
		byOutlet[j.Outlet] = j
		// Hard legal invariant: no article body prose is ever stored.
		blob := j.Text + " " + j.QuotedSpan + " " + j.SourceName
		if strings.Contains(blob, "PROSE") || strings.Contains(blob, "lorem") {
			t.Fatalf("job carries article body text: %+v", j)
		}
		if j.ID != j.SourceURL || j.ID == "" || j.CheckedAt == "" {
			t.Errorf("job not self-contained: %+v", j)
		}
	}
	afpJob := byOutlet[hostOf(afp.URL)]
	if afpJob.LiteralVerdict != string(domain.LiteralInaccurate) {
		t.Errorf("afp verdict = %q, want inaccurate", afpJob.LiteralVerdict)
	}
	if afpJob.SourceName != "AFP Factuel" {
		t.Errorf("afp source name = %q", afpJob.SourceName)
	}
	lm := byOutlet[hostOf(lemonde.URL)]
	if lm.LiteralVerdict != string(domain.LiteralAccurate) {
		t.Errorf("lemonde (@graph) verdict = %q, want accurate", lm.LiteralVerdict)
	}
}

func TestRunHonoursRobotsAndPacing(t *testing.T) {
	t.Parallel()
	robots := "User-agent: *\nDisallow: /private/\n"
	var privateHits int32
	srv := outletServer(t, robots, &privateHits,
		article{"/a1", func(u string) string { return standaloneArticle(u, "Claim one", "Faux", "2024-01-01", "") }},
		article{"/a2", func(u string) string { return standaloneArticle(u, "Claim two", "Vrai", "2024-01-02", "") }},
	)
	outlets := []Outlet{{Name: "Outlet", Host: hostOf(srv.URL), Sitemap: srv.URL + "/sitemap.xml"}}
	c := newTestClient(t, Config{Outlets: outlets})

	var sleeps int32
	c.sleep = func(_ context.Context, _ time.Duration) error {
		atomic.AddInt32(&sleeps, 1)
		return nil
	}
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 2 {
		t.Fatalf("published = %d, want 2", stats.Published)
	}
	// The disallowed /private/secret page is counted skipped and never fetched, and
	// two article fetches means one inter-fetch pacing sleep.
	if atomic.LoadInt32(&privateHits) != 0 {
		t.Errorf("robots-disallowed page was fetched %d times, want 0", privateHits)
	}
	if stats.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the robots-disallowed page)", stats.Skipped)
	}
	if atomic.LoadInt32(&sleeps) != 1 {
		t.Errorf("paced %d times, want 1 between the two fetches", sleeps)
	}
}

func TestRunSkipsRestrictiveSdLicenseAndUnmappedIsUnverifiable(t *testing.T) {
	t.Parallel()
	robots := "User-agent: *\n"
	srv := outletServer(t, robots, nil,
		article{"/restricted", func(u string) string {
			return standaloneArticle(u, "Restricted claim", "Faux", "2024-01-01",
				`,"sdLicense":"https://creativecommons.org/licenses/by-nd/4.0/"`)
		}},
		article{"/unmapped", func(u string) string {
			// No numeric scale and an unmappable textual rating -> unverifiable.
			return `<html><head><script type="application/ld+json">{"@context":"https://schema.org","@type":"ClaimReview","claimReviewed":"Un chiffre","datePublished":"2024-05-05","url":"` + u + `","author":{"@type":"Organization","name":"Outlet"},"reviewRating":{"@type":"Rating","alternateName":"Non catégorisé"}}</script></head><body>x</body></html>`
		}},
	)
	outlets := []Outlet{{Name: "Outlet", Host: hostOf(srv.URL), Sitemap: srv.URL + "/sitemap.xml"}}
	c := newTestClient(t, Config{Outlets: outlets})
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Published != 1 || stats.Unverifiable != 1 {
		t.Fatalf("stats = %+v, want Published=1 Unverifiable=1 (restrictive one skipped)", stats)
	}
	if stats.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the by-nd record)", stats.Skipped)
	}
	if got := pub.jobs(t)[0].LiteralVerdict; got != string(domain.LiteralUnverifiable) {
		t.Errorf("unmapped verdict = %q, want unverifiable", got)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{MaxPriority: 0, Outlets: []Outlet{{Host: "h", Sitemap: "s"}}}); err == nil {
		t.Error("accepted zero priority")
	}
	if _, err := New(Config{MaxPriority: 1}); err == nil {
		t.Error("accepted empty outlet list")
	}
	if _, err := New(Config{MaxPriority: 1, Outlets: []Outlet{{Name: "x"}}}); err == nil {
		t.Error("accepted outlet without host/sitemap")
	}
}
