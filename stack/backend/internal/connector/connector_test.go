package connector

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

var update = flag.Bool("update", false, "regenerate sources.json from the registry")

func TestRegistryValidates(t *testing.T) {
	t.Parallel()
	if err := Validate(); err != nil {
		t.Fatalf("registry does not validate: %v", err)
	}
}

// TestValidateRejectsDuplicateName guards the name-uniqueness invariant.
func TestValidateRejectsDuplicateName(t *testing.T) {
	t.Parallel()
	ds := []Descriptor{
		{Name: "a", Producer: "pa", Worker: "wa", Queue: "qa"},
		{Name: "a", Producer: "pb", Worker: "wb", Queue: "qb"},
	}
	if err := validateDescriptors(ds); err == nil {
		t.Fatal("validateDescriptors accepted a duplicate name, want error")
	}
}

// TestValidateRejectsPrefixCollision guards that two names normalizing to the same
// SCHEDULE_<PREFIX> are rejected, so they cannot silently share one enable/cron
// knob.
func TestValidateRejectsPrefixCollision(t *testing.T) {
	t.Parallel()
	ds := []Descriptor{
		{Name: "insee-series", DefaultCron: "0 3 * * *", Producer: "p1", Worker: "w1", Queue: "q1"},
		{Name: "insee_series", DefaultCron: "0 4 * * *", Producer: "p2", Worker: "w2", Queue: "q2"},
	}
	if a, b := (Descriptor{Name: "insee-series"}).EnvPrefix(), (Descriptor{Name: "insee_series"}).EnvPrefix(); a != b {
		t.Fatalf("test premise broken: prefixes differ (%q vs %q)", a, b)
	}
	if err := validateDescriptors(ds); err == nil {
		t.Fatal("validateDescriptors accepted a prefix collision, want error")
	}
}

// exampleDescriptor is the test-only descriptor for the internal/example template.
// The template is deliberately kept out of the live registry so no operator action
// can run it against a real environment; this proves the registry entry a real
// source copies is well-formed and that it stays consistent with the live registry
// (distinct name and env prefix).
func exampleDescriptor() Descriptor {
	return Descriptor{
		Name:        "example",
		DefaultCron: "0 5 * * *",
		Producer:    "examplecrawl",
		Worker:      "exampleworker",
		Queue:       "example.evidence",
		RequiredEnv: []string{"EXAMPLE_LABEL"},
		ForwardEnv:  []string{"EXAMPLE_LABEL", "EXAMPLE_MAX_ITEMS"},
		NewQueue:    true,
	}
}

func TestExampleTemplateIsNotLiveButValid(t *testing.T) {
	t.Parallel()
	if _, ok := Lookup("example"); ok {
		t.Fatal("example must not be in the live registry (an operator could run it against real infra)")
	}
	ex := exampleDescriptor()
	if err := ex.Validate(); err != nil {
		t.Fatalf("example template descriptor does not validate: %v", err)
	}
	// The template descriptor plus the live registry must still be a valid set:
	// distinct name and env prefix, so the recipe a real source copies never
	// collides with an existing source.
	if err := validateDescriptors(append(All(), ex)); err != nil {
		t.Fatalf("example template collides with the live registry: %v", err)
	}
}

func TestRegistryHasTheFourProductionSources(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"wikipedia", "stats", "factcheck", "scrutins"} {
		if _, ok := Lookup(name); !ok {
			t.Errorf("production source %q missing from the registry", name)
		}
	}
}

// TestProductionSourcesUnchanged pins the migrated sources' scheduler and cloud
// wiring to their pre-registry values, so the migration cannot silently change a
// cron, a queue, or the forwarded env of a live source.
func TestProductionSourcesUnchanged(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		defaultCron string
		producer    string
		worker      string
		queue       string
		schedulable bool
	}{
		{"wikipedia", "0 3 * * *", "wikicrawl", "crawlworker", "crawl.chunks", true},
		{"stats", "", "statsingest", "embedworker", "embedding.jobs", false},
		{"factcheck", "0 4 * * *", "factcheckcrawl", "factcheckworker", "factcheck.claims", true},
		{"scrutins", "30 4 * * *", "scrutinscrawl", "scrutinsworker", "scrutins.votes", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, ok := Lookup(tt.name)
			if !ok {
				t.Fatalf("source %q missing", tt.name)
			}
			if d.DefaultCron != tt.defaultCron {
				t.Errorf("cron = %q, want %q", d.DefaultCron, tt.defaultCron)
			}
			if d.Producer != tt.producer || d.Worker != tt.worker || d.Queue != tt.queue {
				t.Errorf("wiring = %s/%s/%s, want %s/%s/%s", d.Producer, d.Worker, d.Queue, tt.producer, tt.worker, tt.queue)
			}
			if d.Schedulable() != tt.schedulable {
				t.Errorf("schedulable = %v, want %v", d.Schedulable(), tt.schedulable)
			}
		})
	}
}

// TestNoSecretIsForwarded is the AC5 guard: a declared secret is read only from
// Secrets Manager on the host and must never appear in ForwardEnv, or it would
// travel through the operator's SSM command payload.
func TestNoSecretIsForwarded(t *testing.T) {
	t.Parallel()
	for _, d := range All() {
		forwarded := make(map[string]struct{}, len(d.ForwardEnv))
		for _, e := range d.ForwardEnv {
			forwarded[e] = struct{}{}
		}
		for _, s := range d.Secrets {
			if _, ok := forwarded[s.EnvVar]; ok {
				t.Errorf("source %q forwards its secret %q", d.Name, s.EnvVar)
			}
		}
	}
}

func TestEnvPrefix(t *testing.T) {
	t.Parallel()
	tests := map[string]string{"wikipedia": "WIKIPEDIA", "factcheck": "FACTCHECK", "example": "EXAMPLE"}
	for name, want := range tests {
		d := Descriptor{Name: name}
		if got := d.EnvPrefix(); got != want {
			t.Errorf("EnvPrefix(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDescriptorValidateRejects(t *testing.T) {
	t.Parallel()
	base := Descriptor{Name: "x", Producer: "p", Worker: "w", Queue: "q"}
	tests := []struct {
		name string
		mut  func(d *Descriptor)
	}{
		{"empty name", func(d *Descriptor) { d.Name = "" }},
		{"empty producer", func(d *Descriptor) { d.Producer = "" }},
		{"empty worker", func(d *Descriptor) { d.Worker = "" }},
		{"empty queue", func(d *Descriptor) { d.Queue = "" }},
		{"bad cron", func(d *Descriptor) { d.DefaultCron = "not a cron" }},
		{"required not forwarded", func(d *Descriptor) { d.RequiredEnv = []string{"A"} }},
		{"secret forwarded", func(d *Descriptor) {
			d.ForwardEnv = []string{"K"}
			d.Secrets = []SecretRef{{EnvVar: "K", SecretSuffix: "app/k"}}
		}},
		{"secret missing suffix", func(d *Descriptor) { d.Secrets = []SecretRef{{EnvVar: "K"}} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := base
			tt.mut(&d)
			if err := d.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error for %s", tt.name)
			}
		})
	}
}

func TestManifestMatchesEmbedded(t *testing.T) {
	want, err := MarshalManifest()
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	if *update {
		const path = "sources.json"
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write sources.json: %v", err)
		}
		t.Logf("regenerated %s", path)
		return
	}
	if got := EmbeddedManifest(); string(got) != string(want) {
		t.Fatalf("sources.json is stale; run `go test ./internal/connector -run Manifest -update`\n--- embedded ---\n%s\n--- registry ---\n%s", got, want)
	}
}

func TestManifestRoundTrips(t *testing.T) {
	t.Parallel()
	raw, err := MarshalManifest()
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Sources) != len(All()) {
		t.Fatalf("round trip lost sources: got %d, want %d", len(m.Sources), len(All()))
	}
}

func TestEvidenceJobValidate(t *testing.T) {
	t.Parallel()
	valid := EvidenceJob{Source: "insee", ExternalID: "001688370", ChunkIndex: 0, Content: "text", Kind: string(domain.EvidenceKindLead)}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid job rejected: %v", err)
	}
	tests := []struct {
		name string
		mut  func(j *EvidenceJob)
	}{
		{"empty source", func(j *EvidenceJob) { j.Source = "" }},
		{"empty external id", func(j *EvidenceJob) { j.ExternalID = "" }},
		{"negative index", func(j *EvidenceJob) { j.ChunkIndex = -1 }},
		{"empty content", func(j *EvidenceJob) { j.Content = "" }},
		{"bad kind", func(j *EvidenceJob) { j.Kind = "sidebar" }},
		{"negative attempt", func(j *EvidenceJob) { j.Attempt = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			j := valid
			tt.mut(&j)
			if err := j.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error for %s", tt.name)
			}
		})
	}
}

func TestEvidenceJobChunkAndJSONRoundTrip(t *testing.T) {
	t.Parallel()
	job := EvidenceJob{
		Source: "insee", ExternalID: "001688370", ChunkIndex: 2,
		Title: "Chomage", URL: "https://insee.fr/x", Content: "Le taux de chomage...",
		Kind: string(domain.EvidenceKindBody), Metadata: map[string]any{"idbank": "001688370"}, Attempt: 1,
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"external_id":"001688370"`) {
		t.Fatalf("json missing external_id key: %s", raw)
	}
	var back EvidenceJob
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	chunk := back.Chunk()
	if chunk.Source != job.Source || chunk.ExternalID != job.ExternalID || chunk.ChunkIndex != job.ChunkIndex {
		t.Errorf("chunk key = %s/%s/%d, want %s/%s/%d", chunk.Source, chunk.ExternalID, chunk.ChunkIndex, job.Source, job.ExternalID, job.ChunkIndex)
	}
	if chunk.Kind != domain.EvidenceKindBody {
		t.Errorf("chunk kind = %q, want body", chunk.Kind)
	}
	if chunk.Embedding != nil {
		t.Errorf("chunk embedding = %v, want nil (worker fills it)", chunk.Embedding)
	}
	if chunk.Metadata["idbank"] != "001688370" {
		t.Errorf("metadata not carried: %+v", chunk.Metadata)
	}
}
