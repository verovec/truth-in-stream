package hatvp

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

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// hatvpServer serves the real captured index and per-declaration XML fixtures,
// counting index requests so a conditional-GET skip is observable. On the second
// index request it answers 304 when the client sends the recorded ETag.
func hatvpServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	index := readFixture(t, "liste.csv")
	indexHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/liste.csv"):
			indexHits++
			if r.Header.Get("If-None-Match") == "idx-v1" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "idx-v1")
			w.Header().Set("Content-Type", "text/csv")
			_, _ = w.Write(index)
		case strings.HasPrefix(r.URL.Path, "/dossiers/"):
			file := strings.TrimPrefix(r.URL.Path, "/dossiers/")
			data, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &indexHits
}

func newTestProducer(t *testing.T, srv *httptest.Server, pub Publisher) *Producer {
	t.Helper()
	dir := t.TempDir()
	p, err := New(Config{
		IndexURL:       srv.URL + "/livraison/opendata/liste.csv",
		DossierBaseURL: srv.URL + "/dossiers",
		MarkerPath:     filepath.Join(dir, "marker.json"),
		ManifestPath:   filepath.Join(dir, "manifest.json"),
		MaxPriority:    5,
	}, pub, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// TestRunPublishesDeclarationsWithProvenance drives a full run against the real
// fixtures and asserts each published declaration carries correct attribution,
// provenance, and a valid, well-keyed evidence job.
func TestRunPublishesDeclarationsWithProvenance(t *testing.T) {
	t.Parallel()
	srv, _ := hatvpServer(t)
	pub := &capturePublisher{}
	p := newTestProducer(t, srv, pub)

	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New == 0 {
		t.Fatal("Run published no declarations from the real fixtures")
	}
	if len(pub.jobs) == 0 {
		t.Fatal("no jobs published")
	}
	sawParticipation := false
	for _, j := range pub.jobs {
		if j.Source != Source {
			t.Errorf("job source = %q, want %q", j.Source, Source)
		}
		if j.ExternalID == "" || !strings.HasSuffix(j.ExternalID, ".xml") {
			t.Errorf("job external id = %q, want the declaration xml file name", j.ExternalID)
		}
		if !strings.HasPrefix(j.URL, "https://www.hatvp.fr") {
			t.Errorf("job url = %q, want the HATVP nominative page", j.URL)
		}
		if err := j.Validate(); err != nil {
			t.Errorf("job for %q does not validate: %v", j.ExternalID, err)
		}
		if j.Kind == string(domain.EvidenceKindLead) && !strings.Contains(j.Content, "LAHMAR") && !strings.Contains(j.Content, "Lahmar") && !strings.Contains(j.Content, "Abdelkader") {
			t.Errorf("lead passage does not name the official: %q", j.Content)
		}
		if strings.Contains(j.Content, "Participations à des organes dirigeants") {
			sawParticipation = true
		}
		if strings.Contains(j.Content, "[Données non publiées]") {
			t.Errorf("job leaked a withheld field: %q", j.Content)
		}
	}
	if !sawParticipation {
		t.Error("expected the dia declaration's participation dirigeante section in a passage")
	}
}

// TestRunIsIdempotent proves a second run over an unchanged index publishes
// nothing (the conditional GET short-circuits) - AC: re-runs are idempotent.
func TestRunIsIdempotent(t *testing.T) {
	t.Parallel()
	srv, indexHits := hatvpServer(t)
	pub := &capturePublisher{}
	p := newTestProducer(t, srv, pub)

	if _, err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run (first): %v", err)
	}
	firstCount := len(pub.jobs)
	if firstCount == 0 {
		t.Fatal("first run published nothing")
	}
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run (second): %v", err)
	}
	if stats.New != 0 {
		t.Errorf("second run published %d declarations, want 0", stats.New)
	}
	if len(pub.jobs) != firstCount {
		t.Errorf("second run published extra jobs: %d -> %d", firstCount, len(pub.jobs))
	}
	if *indexHits != 2 {
		t.Errorf("index requested %d times, want 2 (one per run)", *indexHits)
	}
}

// TestRunSkipsUnfetchableDeclaration proves a declaration whose XML 404s is
// skipped without stranding the run or recording its fingerprint.
func TestRunSkipsUnfetchableDeclaration(t *testing.T) {
	t.Parallel()
	// Serve the index but 404 every dossier, so every row fails to fetch.
	index := readFixture(t, "liste.csv")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/liste.csv") {
			_, _ = w.Write(index)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	pub := &capturePublisher{}
	p := newTestProducer(t, srv, pub)
	stats, err := p.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 0 {
		t.Errorf("published %d despite all fetches failing, want 0", stats.New)
	}
	if len(pub.jobs) != 0 {
		t.Errorf("published %d jobs despite all fetches failing", len(pub.jobs))
	}
}

// TestParseIndexKeepsOnlyDeliveredRows checks the index parser drops rows that
// name no declaration file or are not yet delivered.
func TestParseIndexKeepsOnlyDeliveredRows(t *testing.T) {
	t.Parallel()
	const csv = "civilite;prenom;nom;type_document;open_data;statut_publication;url_dossier\n" +
		"M.;A;B;dia;a-dia.xml;Livrée;/pages_nominatives/a\n" +
		"M.;C;D;di;;En cours;/pages_nominatives/c\n" +
		"Mme;E;F;dia;f-dia.xml;En cours;/pages_nominatives/f\n"
	rows, err := parseIndex(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("parseIndex: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("kept %d rows, want 1 (only the delivered one with a file)", len(rows))
	}
	if rows[0].OpenDataFile != "a-dia.xml" {
		t.Errorf("kept the wrong row: %+v", rows[0])
	}
}

// TestRenderNeantSection checks a section flagged neant renders the explicit
// "néant" line, so "declared nothing" is itself a checkable fact.
func TestRenderNeantSection(t *testing.T) {
	t.Parallel()
	decl, err := parseDeclaration(readFixture(t, "lahmar-abdelkader-dia31320-depute-69.xml"))
	if err != nil {
		t.Fatalf("parseDeclaration: %v", err)
	}
	row := indexRow{Prenom: "Abdelkader", Nom: "LAHMAR", Qualite: "Député du Rhône", TypeDocument: "dia", URLDossier: "/pages_nominatives/lahmar-abdelkader-27452"}
	passage := render(row, decl)
	if !strings.Contains(passage, "Mandats électifs : néant") {
		t.Errorf("expected an explicit neant line for the empty mandate section: %q", passage)
	}
}
