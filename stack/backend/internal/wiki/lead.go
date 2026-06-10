// Package wiki turns Wikipedia multistream dumps into chunked plain-text
// suitable for embedding: dump parsing, lead-section extraction, wikitext
// stripping, and paragraph-boundary chunking.
package wiki

import (
	"regexp"
	"strings"
)

// headingRe matches the first section heading at the start of a line; the
// lead section is everything before it.
var headingRe = regexp.MustCompile(`(?m)^={2,}`)

// refRe matches self-closing <ref ... /> tags and <ref>...</ref> pairs, in
// that order so a self-closing ref whose attributes contain "/" (e.g.
// name="nyt/1921") is never mistaken for the opening tag of a pair, which
// would swallow prose up to the next closing tag. The (?:\s[^>]*)? boundary
// keeps other tags like <references/> from matching. Reference bodies are
// citations, not prose, so they are dropped entirely.
var refRe = regexp.MustCompile(`(?is)<ref(?:\s[^>]*)?/>|<ref(?:\s[^>]*)?>.*?</ref>`)

// commentRe matches HTML comments, which carry editor notes, not content.
var commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// tagRe matches any remaining HTML tag once refs and comments are gone.
var tagRe = regexp.MustCompile(`(?s)<[^>]+>`)

// externalLinkRe matches [url label] and bare [url] external links.
var externalLinkRe = regexp.MustCompile(`\[(?:https?|ftp)://[^\s\]]+([^\]]*)\]`)

// spaceRe collapses runs of spaces and tabs left behind by removals.
var spaceRe = regexp.MustCompile(`[ \t]+`)

// blankRe collapses three or more newlines to one paragraph break.
var blankRe = regexp.MustCompile(`\n{3,}`)

// disambigRe matches the disambiguation-family templates used on English and
// Simple English Wikipedia (the {{disambiguation}} aliases plus the
// topic-specific "<topic> disambiguation" variants and the __DISAMBIG__ magic
// word; see Module:Disambiguation/templates). The name must be followed by
// "|" or "}}" so short aliases cannot match prefixes of other templates.
var disambigRe = regexp.MustCompile(`(?i)\{\{\s*(?:disambiguation|disambig|disamb|dab|dis|hndis|geodis|numberdis|schooldis|hospitaldis|mathdab|roaddis|letter-number ?comb(?:ination )?dis(?:ambiguation)?|[a-z ]+ disambiguation)\s*(?:\||\}\})|__DISAMBIG__`)

// ExtractLead returns the article's lead section - the wikitext before the
// first heading - stripped of markup down to plain paragraphs separated by
// blank lines. It returns "" when nothing readable remains.
func ExtractLead(wikitext string) string {
	lead := wikitext
	if loc := headingRe.FindStringIndex(lead); loc != nil {
		lead = lead[:loc[0]]
	}

	lead = commentRe.ReplaceAllString(lead, "")
	lead = refRe.ReplaceAllString(lead, "")
	lead = stripTemplates(lead)
	lead = stripBracketedLinks(lead)
	lead = externalLinkRe.ReplaceAllString(lead, "$1")
	lead = strings.ReplaceAll(lead, "'''", "")
	lead = strings.ReplaceAll(lead, "''", "")
	lead = tagRe.ReplaceAllString(lead, "")

	return tidyWhitespace(lead)
}

// IsDisambiguation reports whether the wikitext carries one of the
// disambiguation templates. Matching requires actual template syntax so prose
// mentioning the word does not count.
func IsDisambiguation(wikitext string) bool {
	return disambigRe.MatchString(wikitext)
}

// stripTemplates removes {{...}} blocks, tracking nesting depth so templates
// embedded in template arguments are removed with their parent.
func stripTemplates(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	depth := 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			depth++
			i++
		case depth > 0 && strings.HasPrefix(s[i:], "}}"):
			depth--
			i++
		case depth == 0:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// stripBracketedLinks rewrites [[...]] links: media and category links are
// dropped wholly (including links nested in their captions), ordinary links
// keep their display label.
func stripBracketedLinks(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if !strings.HasPrefix(s[i:], "[[") {
			b.WriteByte(s[i])
			continue
		}
		end, ok := matchLink(s, i)
		if !ok {
			b.WriteByte(s[i])
			continue
		}
		inner := s[i+2 : end-2]
		if !isMediaLink(inner) {
			b.WriteString(linkLabel(inner))
		}
		i = end - 1
	}
	return b.String()
}

// matchLink finds the closing "]]" for the "[[" at start, honoring nested
// links, and returns the index just past it.
func matchLink(s string, start int) (int, bool) {
	depth := 0
	for i := start; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "[["):
			depth++
			i++
		case strings.HasPrefix(s[i:], "]]"):
			depth--
			i++
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// isMediaLink reports whether a link target is a file, image, or category
// reference rather than an article link.
func isMediaLink(inner string) bool {
	prefix, _, ok := strings.Cut(inner, ":")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(prefix)) {
	case "file", "image", "category":
		return true
	default:
		return false
	}
}

// linkLabel returns the display text of an ordinary [[target|label]] link.
// MediaWiki splits on the first pipe - later pipes are part of the label -
// and links without a label display their target. A label can itself contain
// links (matchLink spans them), so the label is stripped recursively.
func linkLabel(inner string) string {
	if _, label, ok := strings.Cut(inner, "|"); ok {
		return stripBracketedLinks(label)
	}
	return inner
}

// tidyWhitespace collapses space runs, trims line edges, and normalizes
// paragraph breaks to a single blank line.
func tidyWhitespace(s string) string {
	s = spaceRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	s = strings.Join(lines, "\n")
	s = blankRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
