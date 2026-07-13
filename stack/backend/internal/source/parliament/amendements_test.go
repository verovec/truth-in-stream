package parliament

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// readFixture loads the captured real-shape amendement entry.
func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/amendement.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestAmendementEntriesUnwrapsEnvelope(t *testing.T) {
	t.Parallel()
	objects, err := amendementEntries(readFixture(t))
	if err != nil {
		t.Fatalf("amendementEntries: %v", err)
	}
	if len(objects) != 1 {
		t.Fatalf("got %d objects, want 1", len(objects))
	}
	var probe struct {
		UID string `json:"uid"`
	}
	if err := json.Unmarshal(objects[0], &probe); err != nil {
		t.Fatalf("unmarshal inner object: %v", err)
	}
	if probe.UID != "AMANR5L17PO838901B0324P1D1N002215" {
		t.Errorf("unwrapped uid = %q, want the amendement uid", probe.UID)
	}
}

func TestAmendementEntriesTolerantOfShapes(t *testing.T) {
	t.Parallel()
	bare := `{"uid":"U1"}`
	tests := map[string]struct {
		in   string
		want int
	}{
		"bare object":              {bare, 1},
		"amendement wrapper":       {`{"amendement":` + bare + `}`, 1},
		"single-element as object": {`{"amendements":{"amendement":` + bare + `}}`, 1},
		"aggregate array":          {`{"amendements":{"amendement":[` + bare + `,{"uid":"U2"}]}}`, 2},
		"top-level array":          {`[` + bare + `,{"uid":"U2"}]`, 2},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := amendementEntries([]byte(tc.in))
			if err != nil {
				t.Fatalf("amendementEntries: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("got %d objects, want %d", len(got), tc.want)
			}
		})
	}
}

func TestParseAmendementRendersAttributedPassage(t *testing.T) {
	t.Parallel()
	objects, err := amendementEntries(readFixture(t))
	if err != nil {
		t.Fatalf("amendementEntries: %v", err)
	}
	rec, err := parseAmendement("an-amendements", objects[0])
	if err != nil {
		t.Fatalf("parseAmendement: %v", err)
	}

	if rec.externalID != "AMANR5L17PO838901B0324P1D1N002215" {
		t.Errorf("externalID = %q", rec.externalID)
	}
	if len(rec.jobs) == 0 {
		t.Fatal("no jobs produced")
	}
	lead := rec.jobs[0]
	if lead.ChunkIndex != 0 || lead.Kind != string(domain.EvidenceKindLead) {
		t.Errorf("lead chunk index=%d kind=%q, want 0/lead", lead.ChunkIndex, lead.Kind)
	}
	if lead.Source != "an-amendements" || lead.ExternalID != rec.externalID {
		t.Errorf("job key = %s/%s", lead.Source, lead.ExternalID)
	}
	if lead.URL != "https://www.assemblee-nationale.fr/dyn/opendata/AMANR5L17PO838901B0324P1D1N002215.json" {
		t.Errorf("provenance URL = %q", lead.URL)
	}

	// The attributed passage names the article, author ref, deposit date, and the
	// fate - the checkable facts.
	for _, want := range []string{"Article 3", "PA123456", "PO845420", "2026-06-02", "Adopté"} {
		if !strings.Contains(lead.Content, want) {
			t.Errorf("passage missing %q:\n%s", want, lead.Content)
		}
	}
	// HTML tags and entities are flattened: no markup survives, entities are decoded.
	if strings.ContainsAny(lead.Content, "<>") || strings.Contains(lead.Content, "&eacute;") {
		t.Errorf("passage still carries HTML markup/entities:\n%s", lead.Content)
	}
	if !strings.Contains(lead.Content, "alinéas") || !strings.Contains(lead.Content, "douze mois") {
		t.Errorf("dispositif text not flattened into passage:\n%s", lead.Content)
	}

	// Provenance metadata is carried verbatim for the verifier.
	meta := lead.Metadata
	for k, want := range map[string]string{
		"legislature": "17", "numero": "CL2215", "auteur_ref": "PA123456",
		"groupe_ref": "PO845420", "article": "Article 3", "sort": "Adopté",
		"date_depot": "2026-06-02", "texte_ref": "PION5L17B0324",
	} {
		if meta[k] != want {
			t.Errorf("metadata[%q] = %v, want %q", k, meta[k], want)
		}
	}

	// The produced job satisfies the connector contract the worker enforces.
	if err := lead.Validate(); err != nil {
		t.Errorf("rendered job does not validate: %v", err)
	}
}

func TestParseAmendementFingerprintTracksFate(t *testing.T) {
	t.Parallel()
	objects, err := amendementEntries(readFixture(t))
	if err != nil {
		t.Fatalf("amendementEntries: %v", err)
	}
	adopted, err := parseAmendement("an-amendements", objects[0])
	if err != nil {
		t.Fatalf("parseAmendement: %v", err)
	}

	// Flip the fate to Rejeté and re-parse: the fingerprint must move so a changed
	// record is re-published rather than skipped.
	rejected := strings.Replace(string(objects[0]), `"sort": "Adopté"`, `"sort": "Rejeté"`, 1)
	rejectedRec, err := parseAmendement("an-amendements", []byte(rejected))
	if err != nil {
		t.Fatalf("parseAmendement rejected: %v", err)
	}
	if adopted.fingerprint == rejectedRec.fingerprint {
		t.Error("fingerprint did not change when the amendment's fate changed")
	}
}

func TestParseAmendementEmptyUIDRejected(t *testing.T) {
	t.Parallel()
	if _, err := parseAmendement("an-amendements", []byte(`{"uid":""}`)); err == nil {
		t.Error("an amendement with an empty uid must be rejected")
	}
}

func TestSplitChunksBoundsAndOrders(t *testing.T) {
	t.Parallel()
	// A long single-word-free text splits at whitespace into bounded chunks.
	long := strings.Repeat("mot ", 1000) // ~4000 runes
	chunks := splitChunks(long, maxChunkRunes)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for a long passage, got %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > maxChunkRunes {
			t.Errorf("chunk %d has %d runes, exceeds max %d", i, n, maxChunkRunes)
		}
	}
	short := "une phrase courte"
	if got := splitChunks(short, maxChunkRunes); len(got) != 1 || got[0] != short {
		t.Errorf("short text should be one chunk unchanged, got %v", got)
	}
}

func TestParseAmendementLongDispositifChunksLeadThenBody(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("disposition ", 400) // forces > maxChunkRunes
	raw := `{"uid":"U-long","corps":{"contenuAuteur":{"dispositif":"` + long + `"}}}`
	rec, err := parseAmendement("an-amendements", []byte(raw))
	if err != nil {
		t.Fatalf("parseAmendement: %v", err)
	}
	if len(rec.jobs) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(rec.jobs))
	}
	if rec.jobs[0].Kind != string(domain.EvidenceKindLead) {
		t.Errorf("first chunk kind = %q, want lead", rec.jobs[0].Kind)
	}
	for i := 1; i < len(rec.jobs); i++ {
		if rec.jobs[i].Kind != string(domain.EvidenceKindBody) {
			t.Errorf("chunk %d kind = %q, want body", i, rec.jobs[i].Kind)
		}
		if rec.jobs[i].ChunkIndex != i {
			t.Errorf("chunk %d has index %d", i, rec.jobs[i].ChunkIndex)
		}
	}
}

func TestPlainTextFlattensHTML(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in   string
		want string
	}{
		"real tags":       {"<p>Bonjour <b>monde</b></p>", "Bonjour monde"},
		"entity-encoded":  {"&lt;p&gt;Bonjour&lt;/p&gt;", "Bonjour"},
		"named entities":  {"alin&eacute;a &agrave; suivre", "alinéa à suivre"},
		"collapse spaces": {"a\n\n  b\t c", "a b c"},
		"empty":           {"", ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := plainText(tc.in); got != tc.want {
				t.Errorf("plainText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
