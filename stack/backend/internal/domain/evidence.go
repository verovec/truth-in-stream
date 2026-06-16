package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// evidenceIDSep separates the three fields of a composed evidence id. Kind is
// the first field and chunk index the last; the source id is everything in
// between, so a source id that itself contains the separator still round-trips.
const evidenceIDSep = ":"

// ComposeEvidenceID builds a stable identifier for one retrieved passage from
// its corpus kind, source row id, and chunk index, so a verifier's citation can
// round-trip back to the exact source. A curated claim has a single text and no
// chunking, so callers pass chunk index 0; a Wikipedia passage uses its page id
// as the source and its chunk index within the page. The result is stable for a
// given (kind, sourceID, chunkIndex) and decoded by ParseEvidenceID.
func ComposeEvidenceID(kind MatchKind, sourceID string, chunkIndex int) string {
	return string(kind) + evidenceIDSep + sourceID + evidenceIDSep + strconv.Itoa(chunkIndex)
}

// ParseEvidenceID decodes an evidence id back into its kind, source id, and
// chunk index, the inverse of ComposeEvidenceID. It errors on any id that was
// not produced by ComposeEvidenceID - an unknown kind, an empty source, a
// missing or non-numeric or negative chunk index - so a fabricated id is
// rejected rather than silently resolving to a wrong source.
func ParseEvidenceID(id string) (MatchKind, string, int, error) {
	firstSep := strings.Index(id, evidenceIDSep)
	lastSep := strings.LastIndex(id, evidenceIDSep)
	if firstSep < 0 || firstSep == lastSep {
		return "", "", 0, fmt.Errorf("domain: evidence id %q: want kind:source:chunk", id)
	}

	kind := MatchKind(id[:firstSep])
	if !kind.Valid() {
		return "", "", 0, fmt.Errorf("domain: evidence id %q: unknown kind %q", id, kind)
	}

	source := id[firstSep+len(evidenceIDSep) : lastSep]
	if source == "" {
		return "", "", 0, fmt.Errorf("domain: evidence id %q: empty source", id)
	}

	chunk, err := strconv.Atoi(id[lastSep+len(evidenceIDSep):])
	if err != nil {
		return "", "", 0, fmt.Errorf("domain: evidence id %q: chunk index: %w", id, err)
	}
	if chunk < 0 {
		return "", "", 0, fmt.Errorf("domain: evidence id %q: negative chunk index %d", id, chunk)
	}
	return kind, source, chunk, nil
}
