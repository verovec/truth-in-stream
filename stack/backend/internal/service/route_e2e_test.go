package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/claimtype"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/source"
	"github.com/verovec/truth-in-stream/backend/internal/source/press"
	"github.com/verovec/truth-in-stream/backend/internal/source/stats"
	"github.com/verovec/truth-in-stream/backend/internal/source/voting"
	"github.com/verovec/truth-in-stream/backend/internal/source/websearch"
)

// This is the end-to-end exercise the card requires: the REAL source adapters
// (stats over an INSEE fixture, voting over an in-memory store, press and web
// over a Brave fixture) wired into the service.Router exactly as the capstone
// will wire them, driven across each claim type. It asserts the right adapter is
// hit per type, that a statistic comes back as a full series (not a point) so the
// verifier can see a cherry-pick, that a thin authoritative result broadens to
// web, and that every passage's evidence_id round-trips through the verifier
// passage projection and back via source.ParseEvidenceID. No fakes stand in for
// the adapter layer and no live external call is made.

func readTestdata(t *testing.T, pkgDir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "source", pkgDir, "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s/%s: %v", pkgDir, name, err)
	}
	return b
}

// votingStore is the in-memory voting store the real voting pack reads, returning
// a scripted recorded position for the e2e claim.
type votingStore struct {
	records []domain.VotingRecord
}

func (s *votingStore) LookupVotingRecords(_ context.Context, _, _ string, _ time.Time) ([]domain.VotingRecord, error) {
	return s.records, nil
}

// fixtureServer serves a static body for any request, the stand-in for a real
// keyless statistics or search API in the e2e wiring.
func fixtureServer(t *testing.T, contentType string, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newE2ERouter(t *testing.T) *service.Router {
	t.Helper()

	inseeSrv := fixtureServer(t, "application/xml", readTestdata(t, "stats", "insee_series.xml"))
	braveSrv := fixtureServer(t, "application/json", readTestdata(t, "websearch", "brave_fr.json"))
	pressSrv := fixtureServer(t, "application/json", readTestdata(t, "press", "brave_attribution_fr.json"))

	statsPack := stats.New(stats.Config{INSEEBaseURL: inseeSrv.URL})

	webPack, err := websearch.New(websearch.Config{APIKey: "test-token", BaseURL: braveSrv.URL})
	if err != nil {
		t.Fatalf("websearch.New: %v", err)
	}
	pressPack, err := press.New(press.Config{APIKey: "test-token", BaseURL: pressSrv.URL})
	if err != nil {
		t.Fatalf("press.New: %v", err)
	}

	votedOn := time.Date(2024, 10, 15, 0, 0, 0, 0, time.UTC)
	votingPack := voting.New(&votingStore{records: []domain.VotingRecord{{
		PersonID:   "PA12345",
		PersonName: "Jean Dupont",
		Chamber:    domain.ChamberAssemblee,
		ScrutinID:  "VTANR5L17V42",
		BillTitle:  "Projet de loi de finances pour 2024",
		VotedOn:    votedOn,
		Position:   domain.VoteFor,
		SourceURL:  "https://www.assemblee-nationale.fr/dyn/17/scrutins/42",
	}}})

	router, err := service.NewRouter(
		[]source.Retriever{statsPack, votingPack, pressPack, webPack},
		service.RouterConfig{MinResults: 1, Lang: "fr"},
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return router
}

// roundTrip projects retrieved evidence into verifier passages and confirms every
// passage id parses back to a well-formed evidence id, the citation contract the
// verifier relies on.
func roundTrip(t *testing.T, evidence []source.Evidence) {
	t.Helper()
	passages := service.EvidencePassagesFrom(evidence)
	if len(passages) != len(evidence) {
		t.Fatalf("projected %d passages from %d evidence", len(passages), len(evidence))
	}
	for i, p := range passages {
		if p.ID != evidence[i].ID.String() {
			t.Fatalf("passage %d id %q != evidence id %q", i, p.ID, evidence[i].ID.String())
		}
		parsed, err := source.ParseEvidenceID(p.ID)
		if err != nil {
			t.Fatalf("ParseEvidenceID(%q): %v", p.ID, err)
		}
		if parsed.String() != p.ID {
			t.Fatalf("evidence id did not round-trip: %q -> %q", p.ID, parsed.String())
		}
	}
}

func TestRouterE2EAcrossClaimTypes(t *testing.T) {
	t.Parallel()
	router := newE2ERouter(t)

	t.Run("statistic returns a full series and round-trips", func(t *testing.T) {
		out, err := router.Retrieve(t.Context(), "le chomage est a 7,3%", claimtype.Statistic,
			map[string]string{stats.HintINSEEIDBANK: "001688526"})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(out) != 1 {
			t.Fatalf("want 1 stats evidence, got %d", len(out))
		}
		if out[0].ID.Kind != source.KindStatsINSEE {
			t.Fatalf("statistic did not route to INSEE stats: %q", out[0].ID.Kind)
		}
		// A series, not a point: the fixture carries several observations, so the
		// rendered passage must hold more than one period for the verifier to spot a
		// cherry-pick.
		if strings.Count(out[0].Passage, "\n") < 2 {
			t.Fatalf("stats passage is not a multi-period series: %q", out[0].Passage)
		}
		roundTrip(t, out)
	})

	t.Run("voting routes to the voting store and round-trips", func(t *testing.T) {
		out, err := router.Retrieve(t.Context(), "Jean Dupont a vote pour le budget 2024", claimtype.VotingRecord,
			map[string]string{
				voting.HintPersonID: "PA12345",
				voting.HintBill:     "Projet de loi de finances pour 2024",
				voting.HintVotedOn:  "2024-10-15",
			})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(out) != 1 || out[0].ID.Kind != source.KindVotingRecord {
			t.Fatalf("voting claim did not route to the voting store: %+v", out)
		}
		if !strings.Contains(out[0].Passage, "a vote pour") {
			t.Fatalf("voting passage missing recorded position: %q", out[0].Passage)
		}
		roundTrip(t, out)
	})

	t.Run("attribution routes to press and round-trips", func(t *testing.T) {
		out, err := router.Retrieve(t.Context(), "Le ministre a declare que la reforme est adoptee", claimtype.Attribution, nil)
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(out) == 0 {
			t.Fatalf("attribution returned no press evidence")
		}
		if out[0].ID.Kind != source.KindAttribution {
			t.Fatalf("attribution did not route to press: %q", out[0].ID.Kind)
		}
		roundTrip(t, out)
	})

	t.Run("causal routes to web and round-trips", func(t *testing.T) {
		out, err := router.Retrieve(t.Context(), "la reforme a cause une hausse du chomage", claimtype.Causal, nil)
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(out) == 0 || out[0].ID.Kind != source.KindWebSearch {
			t.Fatalf("causal claim did not route to web: %+v", out)
		}
		roundTrip(t, out)
	})

	t.Run("thin authoritative result broadens to web", func(t *testing.T) {
		// A statistic with no series hint: the stats pack returns nothing (thin), so
		// the router broadens to the open-web fallback rather than giving up.
		out, err := router.Retrieve(t.Context(), "le chomage augmente partout", claimtype.Statistic, nil)
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(out) == 0 {
			t.Fatalf("thin statistic did not broaden to web")
		}
		if out[0].ID.Kind != source.KindWebSearch {
			t.Fatalf("thin statistic fallback was not web evidence: %q", out[0].ID.Kind)
		}
		roundTrip(t, out)
	})

	t.Run("opinion is filtered, never retrieved", func(t *testing.T) {
		out, err := router.Retrieve(t.Context(), "cette politique est honteuse", claimtype.Opinion, nil)
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if len(out) != 0 {
			t.Fatalf("opinion was retrieved: %+v", out)
		}
	})
}
