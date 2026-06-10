package wiki

import (
	"strings"
	"unicode/utf8"
)

// Chunk token window. Tokens are estimated, not tokenizer-exact: ~4 chars per
// token is the standard heuristic for English prose and errs small enough for
// a retrieval-window decision. The budget covers the full embedded text,
// title prefix included, so embedding input stays bounded.
const (
	minChunkTokens = 256
	maxChunkTokens = 512
)

// paragraphSep joins pieces that came from different paragraphs.
const paragraphSep = "\n\n"

// estimateTokens approximates the token count of s at 4 characters per token,
// rounding up.
func estimateTokens(s string) int {
	return tokensForLen(len(s))
}

// tokensForLen is estimateTokens on a known byte length, so accumulation
// loops never have to materialize a growing string just to measure it.
func tokensForLen(n int) int {
	return (n + 3) / 4
}

// Chunk splits a stripped lead section into embedding-ready chunks of
// minChunkTokens..maxChunkTokens, split on paragraph boundaries, each
// prefixed with the article title ("{title}\n\n{text}") - the prefix
// measurably improves retrieval for short spoken-segment queries. When a
// paragraph boundary would close a chunk under the floor, the next paragraph
// tops it up at sentence boundaries (word boundaries for sentences that span
// the gap); a paragraph that alone exceeds the budget splits the same way.
// The final chunk may run short when the remaining text cannot fill the
// floor.
func Chunk(title, lead string) []string {
	if strings.TrimSpace(lead) == "" {
		return nil
	}

	prefix := title + paragraphSep
	p := packer{
		floor:  minChunkTokens - estimateTokens(prefix),
		budget: maxChunkTokens - estimateTokens(prefix),
	}
	for _, para := range strings.Split(lead, paragraphSep) {
		p.addParagraph(para)
	}
	p.flush()

	chunks := make([]string, len(p.groups))
	for i, g := range p.groups {
		chunks[i] = prefix + g
	}
	return chunks
}

// packer accumulates text into groups of floor..budget estimated tokens,
// preferring paragraph boundaries, then sentence boundaries, then word
// boundaries.
type packer struct {
	groups []string
	cur    strings.Builder
	floor  int
	budget int
}

func (p *packer) tokens() int {
	return tokensForLen(p.cur.Len())
}

func (p *packer) flush() {
	if p.cur.Len() > 0 {
		p.groups = append(p.groups, strings.TrimSpace(p.cur.String()))
		p.cur.Reset()
	}
}

// write appends s after sep; sep is dropped at the start of a group.
func (p *packer) write(sep, s string) {
	if p.cur.Len() > 0 {
		p.cur.WriteString(sep)
	}
	p.cur.WriteString(s)
}

// addParagraph appends a whole paragraph when it fits, starts a fresh group
// for it when the current one already meets the floor, and otherwise feeds it
// in sentence by sentence.
func (p *packer) addParagraph(para string) {
	sepLen := 0
	if p.cur.Len() > 0 {
		sepLen = len(paragraphSep)
	}
	switch {
	case tokensForLen(p.cur.Len()+sepLen+len(para)) <= p.budget:
		p.write(paragraphSep, para)
	case p.tokens() >= p.floor && tokensForLen(len(para)) <= p.budget:
		p.flush()
		p.write("", para)
	default:
		for j, s := range strings.SplitAfter(para, ". ") {
			sep := ""
			if j == 0 {
				sep = paragraphSep
			}
			p.addSentence(sep, s)
		}
	}
}

// addSentence appends one sentence. A group that already meets the floor
// flushes at the sentence boundary; a group under the floor is topped up to
// the budget at word boundaries so no non-final group is ever emitted under
// the floor while text remains.
func (p *packer) addSentence(sep, s string) {
	for s != "" {
		if tokensForLen(p.cur.Len()+len(sep)+len(s)) <= p.budget {
			p.write(sep, s)
			return
		}
		if p.tokens() >= p.floor {
			p.flush()
			sep = ""
			continue
		}
		free := p.budget*4 - p.cur.Len()
		if p.cur.Len() > 0 {
			free -= len(sep)
		}
		head, tail := cutAtWord(s, free)
		if head == "" {
			if p.cur.Len() == 0 {
				head, tail = cutAtRune(s, free)
			} else {
				p.flush()
				sep = ""
				continue
			}
		}
		p.write(sep, head)
		p.flush()
		sep, s = "", tail
	}
}

// cutAtWord splits s at the last space within the first maxBytes; it returns
// ("", s) when no word boundary fits.
func cutAtWord(s string, maxBytes int) (string, string) {
	if maxBytes <= 0 {
		return "", s
	}
	if len(s) <= maxBytes {
		return s, ""
	}
	i := strings.LastIndexByte(s[:maxBytes], ' ')
	if i <= 0 {
		return "", s
	}
	return s[:i], strings.TrimLeft(s[i:], " ")
}

// cutAtRune hard-splits a space-free run at the rune boundary at or before
// maxBytes - the last resort for a single token longer than the whole budget.
func cutAtRune(s string, maxBytes int) (string, string) {
	if len(s) <= maxBytes {
		return s, ""
	}
	cut := max(maxBytes, 1)
	for cut > 1 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], s[cut:]
}
