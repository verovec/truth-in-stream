package websearch

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/source"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func TestRetrieveFrenchPassages(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "brave_fr.json")

	var gotToken, gotLang, gotCountry string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		gotLang = r.URL.Query().Get("search_lang")
		gotCountry = r.URL.Query().Get("country")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	pack, err := New(Config{APIKey: "secret-token", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ev, err := pack.Retrieve(t.Context(), source.Query{Text: "investissement public France 2024"})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	if gotToken != "secret-token" {
		t.Errorf("subscription token not sent: got %q", gotToken)
	}
	if gotLang != "fr" {
		t.Errorf("search_lang: got %q want fr", gotLang)
	}
	if gotCountry != "FR" {
		t.Errorf("country: got %q want FR", gotCountry)
	}

	if len(ev) != 2 {
		t.Fatalf("want 2 evidence, got %d", len(ev))
	}

	first := ev[0]
	if first.Source.URL != "https://www.insee.fr/fr/statistiques/investissement-public-2024" {
		t.Errorf("source url: got %q", first.Source.URL)
	}
	if first.Source.Name != sourceName {
		t.Errorf("source name: got %q", first.Source.Name)
	}
	if first.Source.Date != "2024-09-12" {
		t.Errorf("source date: got %q", first.Source.Date)
	}
	if !strings.Contains(first.Passage, "3,9 % du PIB") {
		t.Errorf("passage missing description:\n%s", first.Passage)
	}
	if !strings.Contains(first.Passage, "1,8 % en volume") {
		t.Errorf("passage missing extra snippet:\n%s", first.Passage)
	}

	// evidence_id round-trips and is keyed by host.
	rt, err := source.ParseEvidenceID(first.ID.String())
	if err != nil {
		t.Fatalf("ParseEvidenceID: %v", err)
	}
	if rt != first.ID {
		t.Errorf("evidence_id round trip: got %+v want %+v", rt, first.ID)
	}
	if first.ID.Kind != source.KindWebSearch || first.ID.SourceID != "www.insee.fr" {
		t.Errorf("evidence_id components: got %+v", first.ID)
	}
	if ev[1].ID.Index == first.ID.Index {
		t.Errorf("evidence indices not distinct")
	}
}

func TestRetrieveEmptyQuery(t *testing.T) {
	t.Parallel()
	pack, err := New(Config{APIKey: "x"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ev, err := pack.Retrieve(t.Context(), source.Query{Text: "  "})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 0 {
		t.Fatalf("want no evidence for empty query, got %d", len(ev))
	}
}

func TestRetrieveQueryLangOverridesDefault(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "brave_fr.json")
	var gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLang = r.URL.Query().Get("search_lang")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	pack, err := New(Config{APIKey: "x", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := pack.Retrieve(t.Context(), source.Query{Text: "claim", Lang: "en"}); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if gotLang != "en" {
		t.Errorf("query lang not applied: got %q", gotLang)
	}
}

func TestRetrieveNon200(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	pack, err := New(Config{APIKey: "x", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := pack.Retrieve(t.Context(), source.Query{Text: "claim"}); err == nil {
		t.Fatalf("want error on 429")
	}
}

func TestRetrieveTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	pack, err := New(Config{APIKey: "x", BaseURL: srv.URL, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := pack.Retrieve(t.Context(), source.Query{Text: "claim"}); err == nil {
		t.Fatalf("want timeout error")
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatalf("want error when API key empty")
	}
}

func TestLoadConfigRequiresKey(t *testing.T) {
	t.Setenv(envAPIKey, "")
	if _, err := LoadConfig(); err == nil {
		t.Fatalf("want error when key unset")
	}
}

func TestLoadConfigErrorOmitsKey(t *testing.T) {
	t.Setenv(envAPIKey, "super-secret-value")
	t.Setenv(envTimeout, "not-a-duration")
	_, err := LoadConfig()
	if err == nil {
		t.Fatalf("want error on bad duration")
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Fatalf("error leaked the API key: %v", err)
	}
}
