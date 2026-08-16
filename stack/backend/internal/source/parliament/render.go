package parliament

import (
	"html"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// maxChunkRunes bounds one rendered evidence chunk. A long record (an amendment
// dispositif, a debate speech) is split into several chunks at whitespace
// boundaries so each embedded passage stays a coherent size, matching the corpus's
// chunk-per-passage convention; the lead chunk carries the attribution, later
// chunks the continuation text.
const maxChunkRunes = 1200

// record is one parsed dump record: the stable identifier the manifest diffs on,
// the content fingerprint, and the evidence jobs it renders into (one per chunk).
type record struct {
	externalID  string
	fingerprint string
	jobs        []connector.EvidenceJob
}

// buildEvidenceRecord renders an attributed passage into a record: it chunks the
// content to the corpus convention (lead chunk first, body chunks after), stamps
// each chunk with the shared (source, externalID) key and provenance metadata, and
// fingerprints the title+content so any change re-publishes the record while an
// unchanged one is skipped. content must be non-empty (an attribution line at
// minimum) so every record emits at least one valid chunk.
func buildEvidenceRecord(source, externalID, title, url, content string, publishedAt *time.Time, meta map[string]any) record {
	texts := splitChunks(content, maxChunkRunes)
	jobs := make([]connector.EvidenceJob, 0, len(texts))
	for i, text := range texts {
		kind := domain.EvidenceKindBody
		if i == 0 {
			kind = domain.EvidenceKindLead
		}
		jobs = append(jobs, connector.EvidenceJob{
			Source:      source,
			ExternalID:  externalID,
			ChunkIndex:  i,
			Title:       title,
			URL:         url,
			Content:     text,
			Kind:        string(kind),
			Metadata:    meta,
			PublishedAt: publishedAt,
		})
	}
	return record{externalID: externalID, fingerprint: fingerprint(title, content), jobs: jobs}
}

// putMeta writes k=v into meta only when v is non-empty, so a record missing a
// field carries no null metadata key.
func putMeta(meta map[string]any, k, v string) {
	if v != "" {
		meta[k] = v
	}
}

// plainText flattens an HTML fragment (parliamentary texts are stored as HTML,
// sometimes entity-encoded) to a single line of plain French text: it decodes
// entities, strips tags, decodes any entities the tags revealed, and collapses
// whitespace.
func plainText(s string) string {
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

// splitChunks splits text into chunks of at most maxRunes runes, breaking at the
// last whitespace before the limit so a chunk never cuts a word. A short text
// yields a single chunk.
func splitChunks(text string, maxRunes int) []string {
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

// documentDate reads a parliamentary feed date whose first ten characters are
// an ISO YYYY-MM-DD day; anything else (the French prose seance labels) stays
// nil so an unparseable date is never guessed into the typed column.
func documentDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return nil
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return nil
	}
	return &t
}

// seanceDate reads the compact numeric dateSeance stamp (YYYYMMDD followed by
// time digits) the comptes-rendus metadata carries; the human prose
// dateSeanceJour is not machine-dated, so the compact form is the typed date.
func seanceDate(s string) *time.Time {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return nil
	}
	t, err := time.Parse("20060102", s[:8])
	if err != nil {
		return nil
	}
	return &t
}
