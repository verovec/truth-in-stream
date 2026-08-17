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
//
// Contribution is the stance-bearing weight this match added to the statement's
// Confidence aggregate: its Similarity for a curated claim (counted into
// Supporting unless its Verdict contradicts, when it counts into Contradicting),
// its Similarity scaled by the chunk-kind weight for evidence (always
// Supporting), and 0 when the match carried no stance - an unclear claim, a
// non-positive similarity, or a match ranked beyond the scored cluster cap. Kind
// and Verdict say which side it fell on; Contribution is the magnitude, so the
// score is explainable down to the individual match that produced it.
//
// EvidenceID is the passage's stable source coordinate (ComposeEvidenceID over
// kind + source id + chunk index). It lets the retrieve-then-verify path's
// verifier cite a passage by id and have that citation round-trip back to the
// match the UI renders. It is additive and omitted when empty, so the old path
// (which does not set it) and a client that does not read it are unaffected.
type SegmentMatch struct {
	Kind         MatchKind `json:"kind"`
	Claim        string    `json:"claim"`
	Verdict      Verdict   `json:"verdict,omitempty"`
	Sources      []Source  `json:"sources"`
	Similarity   float64   `json:"similarity"`
	Article      *Article  `json:"article,omitempty"`
	Contribution float64   `json:"contribution"`
	EvidenceID   string    `json:"evidence_id,omitempty"`
	// PublishedAt is the matched passage's publication date when the corpus
	// knows one; additive on the wire so old snapshots decode unchanged.
	PublishedAt *time.Time `json:"published_at,omitempty"`
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

// ClaimSpan locates the verbatim words an atomic claim was extracted from
// inside one transcript segment: the segment's correlation id (the subtitle id
// the client keys statement rows on) and the [Start, End) offsets of the quoted
// words within that segment's text. Offsets count runes, not bytes, so a
// JavaScript client maps them onto its own string indices by code point rather
// than by UTF-8 byte. The json tags are the claims frame's wire shape, served
// verbatim to the client. A claim whose quote crosses a segment boundary
// carries one span per segment it touches.
type ClaimSpan struct {
	SegmentID string `json:"segment_id"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
}

// Confidence is the corroboration strength of a checked statement, aggregated
// over its retrieved evidence cluster. Score is the bounded [0, 1] fraction of
// stance-bearing evidence weight that corroborates the statement (rendered as a
// percentage); Supporting and Contradicting are the raw aggregated weights it is
// derived from, and EvidenceItems counts how many matches contributed weight, so
// the score is explainable rather than opaque. Its zero value is the honest
// reading for a checked statement whose cluster carries no stance: nothing
// corroborates it, so the score is 0. A statement that was never checked carries
// no Confidence at all (a nil *Confidence), distinct from a checked score of 0.
type Confidence struct {
	Score         float64 `json:"score"`
	Supporting    float64 `json:"supporting"`
	Contradicting float64 `json:"contradicting"`
	EvidenceItems int     `json:"evidence_items"`
}

// SegmentResult is the fact-check outcome for one transcript segment: the shape
// the live source emits per finalized statement and the wire form the handler
// renders into a result frame.
//
// SkipReason distinguishes a segment the check-worthiness gate declined from
// one that was checked: when it is SkipReasonNone the segment was matched and
// Matches holds the (possibly empty) hits; when it is set the segment was
// skipped, Matches is empty, and no verdict is implied.
//
// Confidence is the corroboration score aggregated over Matches; it is set only
// for a checked segment and nil for a skipped one, so an absent score reads as
// "not checked" rather than "no corroboration".
type SegmentResult struct {
	Segment
	Matches    []SegmentMatch
	SkipReason SkipReason
	Confidence *Confidence
}
