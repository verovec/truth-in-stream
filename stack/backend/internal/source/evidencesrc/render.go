package evidencesrc

import (
	"html"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// MaxChunkRunes bounds one rendered evidence chunk. A long record is split into
// several chunks at whitespace boundaries so each embedded passage stays a
// coherent size, matching the corpus's chunk-per-passage convention; the lead
// chunk carries the attribution, later chunks the continuation text.
const MaxChunkRunes = 1200

// Record is one parsed dump record: the stable identifier the manifest diffs on,
// the content fingerprint, and the evidence jobs it renders into (one per chunk).
type Record struct {
	ExternalID  string
	Fingerprint string
	Jobs        []connector.EvidenceJob
}

// BuildRecord renders an attributed passage into a Record: it chunks the content
// to the corpus convention (lead chunk first, body chunks after), stamps each
// chunk with the shared (source, externalID) key and provenance metadata, and
// fingerprints the title+content so any change re-publishes the record while an
// unchanged one is skipped. content must be non-empty (an attribution line at
// minimum) so every record emits at least one valid chunk.
func BuildRecord(source, externalID, title, url, content string, meta map[string]any) Record {
	texts := SplitChunks(content, MaxChunkRunes)
	jobs := make([]connector.EvidenceJob, 0, len(texts))
	for i, text := range texts {
		kind := domain.EvidenceKindBody
		if i == 0 {
			kind = domain.EvidenceKindLead
		}
		jobs = append(jobs, connector.EvidenceJob{
			Source:     source,
			ExternalID: externalID,
			ChunkIndex: i,
			Title:      title,
			URL:        url,
			Content:    text,
			Kind:       string(kind),
			Metadata:   meta,
		})
	}
	return Record{ExternalID: externalID, Fingerprint: Fingerprint(title, content), Jobs: jobs}
}

// PutMeta writes k=v into meta only when v is non-empty, so a record missing a
// field carries no null metadata key.
func PutMeta(meta map[string]any, k, v string) {
	if v != "" {
		meta[k] = v
	}
}

// PlainText flattens an HTML fragment (some open-data texts are stored as HTML,
// sometimes entity-encoded) to a single line of plain French text: it decodes
// entities, strips tags, decodes any entities the tags revealed, and collapses
// whitespace.
func PlainText(s string) string {
	if s == "" {
		return ""
	}
	s = html.UnescapeString(s)
	s = stripTags(s)
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// stripTags removes every <...> tag from s, leaving the text content. A '<' with
// no closing '>' drops the remainder, the safe reading of a truncated fragment.
func stripTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SplitChunks splits text into chunks of at most maxRunes runes, breaking at the
// last whitespace before the limit so a chunk never cuts a word. A short text
// yields a single chunk.
func SplitChunks(text string, maxRunes int) []string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}
	var chunks []string
	for len(runes) > 0 {
		if len(runes) <= maxRunes {
			chunks = append(chunks, strings.TrimSpace(string(runes)))
			break
		}
		cut := maxRunes
		for i := maxRunes; i > maxRunes/2; i-- {
			if runes[i] == ' ' {
				cut = i
				break
			}
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	return chunks
}
