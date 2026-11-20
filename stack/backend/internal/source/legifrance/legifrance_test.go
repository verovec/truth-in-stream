package legifrance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// capturePublisher records every published job body for assertions.
type capturePublisher struct {
	mu   sync.Mutex
	jobs []connector.EvidenceJob
}

func (c *capturePublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	var j connector.EvidenceJob
	if err := json.Unmarshal(body, &j); err != nil {
		return err
	}
	c.mu.Lock()
	c.jobs = append(c.jobs, j)
	c.mu.Unlock()
	return nil
}

// pisteServer serves the PISTE OAuth2 token endpoint and the getArticle endpoint
// from the captured documented fixture, counting token grants so token caching is
// observable.
func pisteServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	article, err := os.ReadFile(filepath.Join("testdata", "get_article.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tokenGrants := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			tokenGrants++
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-123","token_type":"Bearer","expires_in":3600,"scope":"openid"}`))
		case strings.HasSuffix(r.URL.Path, "/consult/getArticle"):
			if r.Header.Get("Authorization") != "Bearer tok-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(article)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &tokenGrants
}

func testConfig(t *testing.T, srv *httptest.Server, creds Credentials, articles []ArticleRef) Config {
	t.Helper()
	return Config{
		Credentials:  creds,
		TokenURL:     srv.URL + "/api/oauth/token",
		APIBaseURL:   srv.URL,
		Articles:     articles,
		ManifestPath: filepath.Join(t.TempDir(), "manifest.json"),
		MaxPriority:  5,
	}
}

// TestRunPublishesArticle drives a full authenticated run and asserts the article
// renders into a valid, well-keyed evidence job carrying the law text and
// provenance.
func TestRunPublishesArticle(t *testing.T) {
	t.Parallel()
	srv, grants := pisteServer(t)
	pub := &capturePublisher{}
	p, err := New(testConfig(t, srv, Credentials{ClientID: "id", ClientSecret: "secret"},
		[]ArticleRef{{ID: "LEGIARTI000033020408", Label: "Code du travail"}}), pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 1 {
		t.Fatalf("published %d articles, want 1", stats.New)
	}
	if len(pub.jobs) != 1 {
		t.Fatalf("captured %d jobs, want 1", len(pub.jobs))
	}
	j := pub.jobs[0]
	if j.Source != Source || j.ExternalID != "LEGIARTI000033020408" {
		t.Errorf("job key = %s/%s, want %s/LEGIARTI000033020408", j.Source, j.ExternalID, Source)
	}
	if j.Kind != string(domain.EvidenceKindLead) {
		t.Errorf("kind = %q, want lead", j.Kind)
	}
	if !strings.HasPrefix(j.URL, articleURLTemplate) {
		t.Errorf("url = %q, want a Legifrance article link", j.URL)
	}
	if !strings.Contains(j.Content, "trente-cinq heures") {
		t.Errorf("passage missing the article text: %q", j.Content)
	}
	if !strings.Contains(j.Content, "L3121-27") {
		t.Errorf("passage missing the article number: %q", j.Content)
	}
	if err := j.Validate(); err != nil {
		t.Errorf("job does not validate: %v", err)
	}
	if *grants != 1 {
		t.Errorf("token granted %d times, want 1 (cached across the run)", *grants)
	}
}

// TestRunIsIdempotent proves a second run over the unchanged article publishes
// nothing (the manifest diff short-circuits) - AC: re-runs are idempotent.
func TestRunIsIdempotent(t *testing.T) {
	t.Parallel()
	srv, _ := pisteServer(t)
	pub := &capturePublisher{}
	cfg := testConfig(t, srv, Credentials{ClientID: "id", ClientSecret: "secret"},
		[]ArticleRef{{ID: "LEGIARTI000033020408", Label: "Code du travail"}})
	p, err := New(cfg, pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run (first): %v", err)
	}
	// A second producer over the same manifest path must republish nothing.
	p2, err := New(cfg, pub, nil)
	if err != nil {
		t.Fatalf("New (second): %v", err)
	}
	stats, err := p2.Run(context.Background())
	if err != nil {
		t.Fatalf("Run (second): %v", err)
	}
	if stats.New != 0 {
		t.Errorf("second run published %d, want 0", stats.New)
	}
	if len(pub.jobs) != 1 {
		t.Errorf("total published jobs = %d, want 1", len(pub.jobs))
	}
}

// TestRunSkipsCleanlyWithoutCredentials is the AC guard: an unprovisioned source
// degrades to a clean, error-free skip that publishes nothing and makes no call.
func TestRunSkipsCleanlyWithoutCredentials(t *testing.T) {
	t.Parallel()
	srv, grants := pisteServer(t)
	pub := &capturePublisher{}
	p, err := New(testConfig(t, srv, Credentials{}, []ArticleRef{{ID: "LEGIARTI000033020408"}}), pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned an error on missing credentials, want clean skip: %v", err)
	}
	if stats.New != 0 || len(pub.jobs) != 0 {
		t.Errorf("skip published work: stats=%+v jobs=%d", stats, len(pub.jobs))
	}
	if *grants != 0 {
		t.Errorf("token requested %d times on a credential-less skip, want 0", *grants)
	}
}

// TestRunSkipsCleanlyWithPartialCredentials checks a half-set credential pair
// (the config-typo case) still skips cleanly without any API call.
func TestRunSkipsCleanlyWithPartialCredentials(t *testing.T) {
	t.Parallel()
	srv, grants := pisteServer(t)
	pub := &capturePublisher{}
	p, err := New(testConfig(t, srv, Credentials{ClientID: "id"}, []ArticleRef{{ID: "LEGIARTI000033020408"}}), pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !p.cfg.Credentials.Partial() {
		t.Fatal("expected the credential pair to read as partial")
	}
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned an error on a partial credential, want clean skip: %v", err)
	}
	if stats.New != 0 || len(pub.jobs) != 0 || *grants != 0 {
		t.Errorf("partial-credential skip did work: stats=%+v jobs=%d grants=%d", stats, len(pub.jobs), *grants)
	}
}

// TestGetArticleDereferencedToNothing checks an empty article response yields a
// nil article (skip), not a bad publish.
func TestGetArticleDereferencedToNothing(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth/token") {
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
			return
		}
		_, _ = w.Write([]byte(`{"executionTime":1,"dereferenced":true,"article":null}`))
	}))
	t.Cleanup(srv.Close)
	pub := &capturePublisher{}
	p, err := New(testConfig(t, srv, Credentials{ClientID: "id", ClientSecret: "s"},
		[]ArticleRef{{ID: "LEGIARTI000000000000"}}), pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 0 || len(pub.jobs) != 0 {
		t.Errorf("published a dereferenced-away article: stats=%+v", stats)
	}
}

// TestGetArticleParsesFixture checks the client decodes the documented getArticle
// shape from the captured fixture.
func TestGetArticleParsesFixture(t *testing.T) {
	t.Parallel()
	srv, _ := pisteServer(t)
	tokens := NewTokenSource(srv.URL+"/api/oauth/token", Credentials{ClientID: "id", ClientSecret: "s"}, srv.Client())
	client := NewClient(srv.URL, tokens, srv.Client())
	art, err := client.GetArticle(context.Background(), "LEGIARTI000033020408")
	if err != nil {
		t.Fatalf("GetArticle: %v", err)
	}
	if art == nil {
		t.Fatal("GetArticle returned nil for a present article")
	}
	if art.Num != "L3121-27" || art.Etat != "VIGUEUR" || !strings.Contains(art.Texte, "trente-cinq") {
		t.Errorf("decoded article = %+v", art)
	}
}
