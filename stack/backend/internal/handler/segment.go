package handler

import "github.com/verovec/truth-in-stream/backend/internal/domain"

// segmentJSON is the per-segment wire form of one domain.SegmentResult:
// timestamps as seconds, matches served verbatim from their domain shape.
// SkipReason is present only when the gate skipped the segment; its absence
// means the segment was checked and Matches (possibly empty) is authoritative.
// Confidence is the corroboration score; it is present only on a checked segment
// and omitted on a skipped one, so an absent score reads as "not checked" rather
// than "no corroboration". The live result frame embeds it, so the streamed
// verdict shape is defined in exactly one place.
type segmentJSON struct {
	Start      float64               `json:"start"`
	End        float64               `json:"end"`
	Text       string                `json:"text"`
	Matches    []domain.SegmentMatch `json:"matches"`
	SkipReason string                `json:"skip_reason,omitempty"`
	Confidence *domain.Confidence    `json:"confidence,omitempty"`
}

// toSegmentJSON is the single home of the per-segment wire shaping. Nil matches
// become an empty array: an empty matches under no skip reason means "checked,
// no confident match", distinct from "not checked". The confidence pointer is
// passed through as-is, so a checked segment carries its score and a skipped one
// omits it.
func toSegmentJSON(r domain.SegmentResult) segmentJSON {
	matches := r.Matches
	if matches == nil {
		matches = []domain.SegmentMatch{}
	}
	return segmentJSON{
		Start:      r.Start.Seconds(),
		End:        r.End.Seconds(),
		Text:       r.Text,
		Matches:    matches,
		SkipReason: string(r.SkipReason),
		Confidence: r.Confidence,
	}
}
