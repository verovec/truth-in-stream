// Package source defines the jurisdiction-agnostic retriever contract the
// verify path uses to fetch authoritative evidence at query time. A SourcePack
// answers a claim with evidence passages, each carrying a stable EvidenceID and
// the provenance (name, url, date) the verifier cites and the citation guard
// checks against. The contract is neutral to jurisdiction and source kind so a
// future US pack drops in by implementing the same interface; nothing here
// knows about France, INSEE, or Brave.
//
// The package is inert until wired: it defines the port and the value types the
// stats and websearch adapters satisfy, and it makes no outbound call itself.
package source

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Kind identifies the family of source a piece of evidence came from. It is the
// first component of an EvidenceID and the routing key the retrieval layer (card
// J) selects an adapter by. New source families add a new constant; the existing
// ones are never renamed, since an EvidenceID minted under an old name must keep
// parsing.
type Kind string

const (
	// KindStats is the adapter-level family for official statistics. The stats
	// pack advertises this so the routing layer (card J) selects it for any
	// statistic claim regardless of which provider ultimately answers; the
	// per-evidence KindStatsINSEE / KindStatsEurostat distinguish the provider
	// inside each EvidenceID.
	KindStats Kind = "stats"
	// KindStatsINSEE is a French national statistics series (INSEE BDM).
	KindStatsINSEE Kind = "insee"
	// KindStatsEurostat is a European statistics series (Eurostat).
	KindStatsEurostat Kind = "eurostat"
	// KindWebSearch is an open web search result.
	KindWebSearch Kind = "websearch"
)

// Source is the provenance of a piece of evidence: the human-readable publisher
// name, the canonical URL a reader can verify it at, and the date the figure or
// passage is dated to (RFC 3339 or a coarser source-native period like "2024-Q1"
// kept as the source presents it). Date is best-effort and may be empty when the
// source does not date the passage.
type Source struct {
	Name string
	URL  string
	Date string
}

// Evidence is one retrieved passage the verifier reads. Passage is the text the
// model sees (a statistic in context, a web snippet); ID is the stable handle the
// verifier cites and the citation guard resolves back to this passage. Source
// carries the provenance shown in the UI.
type Evidence struct {
	ID      EvidenceID
	Passage string
	Source  Source
}

// EvidenceID is the stable, round-trippable handle for a passage. It is
// composed, not opaque, so retrieval and verification can mint and resolve it
// without a shared registry: Kind selects the source family, SourceID is the
// stable per-kind identifier of the underlying series/dataset/result (an INSEE
// IDBANK, a Eurostat dataset code, a result host), and Index is the passage's
// position within that source's returned passages. The triple is unique within a
// single retrieval and deterministic across runs over the same inputs, so the
// same id flows out to the verifier and back into a citation unchanged.
type EvidenceID struct {
	Kind     Kind
	SourceID string
	Index    int
}

// idSeparator joins the three EvidenceID components. ':' cannot appear in a Kind
// (a fixed constant set) nor is it produced for a SourceID, which is sanitized on
// construction, so the encoding is unambiguous to split.
const idSeparator = ":"

// NewEvidenceID constructs an EvidenceID, sanitizing the source id so the encoded
// form round-trips: the separator is not allowed inside a component, so any
// separator in the raw source id is replaced with '_'. Index is clamped to be
// non-negative.
func NewEvidenceID(kind Kind, sourceID string, index int) EvidenceID {
	if index < 0 {
		index = 0
	}
	return EvidenceID{
		Kind:     kind,
		SourceID: strings.ReplaceAll(sourceID, idSeparator, "_"),
		Index:    index,
	}
}

// String encodes the id as "<kind>:<sourceID>:<index>". The form is stable and
// is what a citation carries.
func (e EvidenceID) String() string {
	return string(e.Kind) + idSeparator + e.SourceID + idSeparator + strconv.Itoa(e.Index)
}

// ParseEvidenceID decodes the form String produces. It is the inverse of String
// for any id minted by NewEvidenceID, so an id round-trips through a citation
// unchanged. The kind (a fixed separator-free constant set) is taken up to the
// first separator and the index from after the last, so a SourceID is recovered
// whole even if it contains a separator the sanitizer did not introduce.
func ParseEvidenceID(s string) (EvidenceID, error) {
	first := strings.Index(s, idSeparator)
	last := strings.LastIndex(s, idSeparator)
	if first < 0 || first == last {
		return EvidenceID{}, fmt.Errorf("parsing evidence id %q: want kind:source:index", s)
	}
	index, err := strconv.Atoi(s[last+len(idSeparator):])
	if err != nil {
		return EvidenceID{}, fmt.Errorf("parsing evidence id %q index: %w", s, err)
	}
	if index < 0 {
		return EvidenceID{}, fmt.Errorf("parsing evidence id %q: index must be non-negative", s)
	}
	return EvidenceID{
		Kind:     Kind(s[:first]),
		SourceID: s[first+len(idSeparator) : last],
		Index:    index,
	}, nil
}

// Query is a retrieval request. Text is the atomic, coreference-resolved claim
// the adapter searches for. Lang is the BCP-47 language the evidence should be
// in (empty means the adapter's default); the French packs default to "fr".
// Hints carries optional structured selectors an adapter understands (e.g. a
// statistics series key) without widening the interface per source; an adapter
// ignores hints it does not recognize.
type Query struct {
	Text  string
	Lang  string
	Hints map[string]string
}

// Hint returns the value for key and whether it was present.
func (q Query) Hint(key string) (string, bool) {
	v, ok := q.Hints[key]
	return v, ok
}

// Retriever is the SourcePack contract: given a claim query, return evidence
// passages with stable ids and provenance, or an error. Kind reports the source
// family the pack serves so the routing layer can select it. Implementations are
// outbound clients only and never touch HTTP server types. A pack returns an
// empty slice (not an error) when it simply found nothing for the claim.
type Retriever interface {
	Kind() Kind
	Retrieve(ctx context.Context, q Query) ([]Evidence, error)
}
