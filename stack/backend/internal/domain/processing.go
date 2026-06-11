package domain

import (
	"encoding/json"
	"time"
)

// Segment is one ordered, timestamped span of transcribed speech. Timestamps
// are millisecond precision: transcription APIs emit milliseconds and the
// store persists milliseconds, so finer durations would not round-trip.
//
// Speaker is the diarized speaker label the streaming provider emits per
// committed turn. The live analyzer groups consecutive same-speaker segments
// into one analysis unit so a verdict never blends two speakers.
type Segment struct {
	Start   time.Duration
	End     time.Duration
	Text    string
	Speaker string
}

// LiveTranscript is one transcript revision from the live speech provider: an
// interim (partial) caption still being revised while the speaker talks, or a
// finalized statement once the provider commits it on a detected pause. Both are
// surfaced as live captions; only a finalized transcript carries timestamps and
// is fact-checked. A partial's Segment holds only Text.
type LiveTranscript struct {
	Segment Segment
	Final   bool
}

// MatchKind distinguishes a curated claim match (which carries a verdict) from
// a Wikipedia evidence match (supporting context with attribution, never a
// verdict). It is the discriminator the frontend switches on.
type MatchKind string

const (
	// MatchKindClaim is a hit against the curated claims corpus; it carries a
	// verdict and citation sources.
	MatchKindClaim MatchKind = "claim"
	// MatchKindEvidence is a hit against the Wikipedia corpus; it is supporting
	// context with article attribution and no verdict.
	MatchKindEvidence MatchKind = "evidence"
)

// Valid reports whether k is a known match kind.
func (k MatchKind) Valid() bool {
	switch k {
	case MatchKindClaim, MatchKindEvidence:
		return true
	default:
		return false
	}
}

// Article is the attribution for a Wikipedia evidence match: the source title
// and article URL. CC BY-SA 4.0 requires both whenever a snippet is shown.
type Article struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// SegmentMatch is one ranked hit for a spoken segment, from either the curated
// claims corpus (Kind claim, with Verdict and Sources) or the Wikipedia corpus
// (Kind evidence, with Article and no verdict). Claim holds the matched
// reference text in both cases: the claim statement for claims, the article
// excerpt for evidence. The json tags are the live result frame's wire shape,
// served verbatim to the client.
type SegmentMatch struct {
	Kind       MatchKind `json:"kind"`
	Claim      string    `json:"claim"`
	Verdict    Verdict   `json:"verdict,omitempty"`
	Sources    []Source  `json:"sources"`
	Similarity float64   `json:"similarity"`
	Article    *Article  `json:"article,omitempty"`
}

// UnmarshalJSON decodes a SegmentMatch, defaulting an absent kind to claim so a
// match payload that omits the discriminator decodes as a claim rather than an
// invalid zero kind, matching the frontend normalizer's own default.
func (m *SegmentMatch) UnmarshalJSON(data []byte) error {
	type raw SegmentMatch
	decoded := raw{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Kind == "" {
		decoded.Kind = MatchKindClaim
	}
	*m = SegmentMatch(decoded)
	return nil
}

// SegmentResult is the fact-check outcome for one transcript segment: the shape
// the live source emits per finalized statement and the wire form the handler
// renders into a result frame.
//
// SkipReason distinguishes a segment the check-worthiness gate declined from
// one that was checked: when it is SkipReasonNone the segment was matched and
// Matches holds the (possibly empty) hits; when it is set the segment was
// skipped, Matches is empty, and no verdict is implied.
type SegmentResult struct {
	Segment
	Matches    []SegmentMatch
	SkipReason SkipReason
}
