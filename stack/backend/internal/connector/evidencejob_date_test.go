package connector

import (
	"encoding/json"
	"testing"
	"time"
)

// TestEvidenceJobDateIsAdditiveOnTheWire proves an old queue message without
// published_at decodes to an undated job, and a dated job round-trips through
// JSON and into the chunk, so mixed producer/worker versions never break.
func TestEvidenceJobDateIsAdditiveOnTheWire(t *testing.T) {
	t.Parallel()

	old := []byte(`{"source":"s","external_id":"e","chunk_index":0,"title":"t","url":"u","content":"c","kind":"lead"}`)
	var legacy EvidenceJob
	if err := json.Unmarshal(old, &legacy); err != nil {
		t.Fatalf("decode legacy message: %v", err)
	}
	if legacy.PublishedAt != nil {
		t.Errorf("legacy message PublishedAt = %v, want nil", legacy.PublishedAt)
	}
	if legacy.Chunk().PublishedAt != nil {
		t.Error("legacy chunk carries a date it never had")
	}

	published := time.Date(2025, 7, 17, 0, 0, 0, 0, time.UTC)
	dated := EvidenceJob{Source: "s", ExternalID: "e", ChunkIndex: 0, Title: "t", URL: "u", Content: "c", Kind: "lead", PublishedAt: &published}
	raw, err := json.Marshal(dated)
	if err != nil {
		t.Fatalf("encode dated job: %v", err)
	}
	var decoded EvidenceJob
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode dated job: %v", err)
	}
	if decoded.PublishedAt == nil || !decoded.PublishedAt.Equal(published) {
		t.Errorf("round-tripped PublishedAt = %v, want %v", decoded.PublishedAt, published)
	}
	if got := decoded.Chunk().PublishedAt; got == nil || !got.Equal(published) {
		t.Errorf("chunk PublishedAt = %v, want %v", got, published)
	}
}
