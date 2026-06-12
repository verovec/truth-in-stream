package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestToSegmentJSONConfidence(t *testing.T) {
	t.Parallel()

	base := domain.SegmentResult{
		Segment: domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "claim"},
	}

	t.Run("checked segment carries its score", func(t *testing.T) {
		t.Parallel()
		r := base
		r.Confidence = &domain.Confidence{Score: 0.8, Supporting: 1.2, Contradicting: 0.3, EvidenceItems: 2}

		got := toSegmentJSON(r)
		if got.Confidence == nil {
			t.Fatal("checked segment should carry a confidence")
		}
		if *got.Confidence != *r.Confidence {
			t.Errorf("confidence = %+v, want %+v", *got.Confidence, *r.Confidence)
		}

		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(encoded), `"confidence"`) {
			t.Errorf("encoded frame missing confidence: %s", encoded)
		}
		if !strings.Contains(string(encoded), `"score":0.8`) {
			t.Errorf("encoded frame missing score: %s", encoded)
		}
	})

	t.Run("skipped segment omits confidence", func(t *testing.T) {
		t.Parallel()
		r := base
		r.SkipReason = domain.SkipReasonNotChecked
		r.Confidence = nil

		got := toSegmentJSON(r)
		if got.Confidence != nil {
			t.Errorf("skipped segment should carry no confidence, got %+v", got.Confidence)
		}

		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(encoded), `"confidence"`) {
			t.Errorf("encoded frame should omit confidence: %s", encoded)
		}
	})
}
