package service

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// claimSpans anchors one claim's verbatim quote onto the unit's member
// segments: it locates the quote inside the members' combined text (the exact
// string the decomposer read) and projects the matched range onto each member
// it overlaps, as [start, end) rune offsets local to that member's text. A
// quote that crosses a segment boundary yields one span per segment. An empty
// or unlocatable quote yields nil: the claim is still verified, it just cannot
// be highlighted, which is strictly better than highlighting the wrong words.
func claimSpans(members []unitMember, quote string) []domain.ClaimSpan {
	if strings.TrimSpace(quote) == "" {
		return nil
	}
	start, end, ok := locateQuote(combinedText(members), quote)
	if !ok {
		return nil
	}
	spans := make([]domain.ClaimSpan, 0, len(members))
	// Members sit in the combined rune space at cumulative offsets, each
	// separated from the previous by the single joining space combinedText
	// writes.
	memberStart := 0
	for i, m := range members {
		if i > 0 {
			memberStart++
		}
		memberLen := utf8.RuneCountInString(m.seg.Text)
		spanStart := max(start, memberStart)
		spanEnd := min(end, memberStart+memberLen)
		if spanStart < spanEnd {
			spans = append(spans, domain.ClaimSpan{
				SegmentID: m.id,
				Start:     spanStart - memberStart,
				End:       spanEnd - memberStart,
			})
		}
		memberStart += memberLen
	}
	if len(spans) == 0 {
		return nil
	}
	return spans
}

// locateQuote finds quote inside text and returns its [start, end) rune
// offsets. It tries the exact substring first, then a tolerant match that
// case-folds and collapses whitespace runs on both sides (speech-to-text output
// and a model's copy of it routinely disagree on casing and spacing), mapping
// the normalized hit back onto the original rune offsets. It reports ok=false
// when the quote does not appear even normalized - the caller then skips the
// highlight rather than guessing.
func locateQuote(text, quote string) (start, end int, ok bool) {
	if quote == "" {
		return 0, 0, false
	}
	if i := strings.Index(text, quote); i >= 0 {
		start = utf8.RuneCountInString(text[:i])
		return start, start + utf8.RuneCountInString(quote), true
	}

	normText, index := normalizeForSearch(text)
	normQuote, _ := normalizeForSearch(quote)
	if normQuote == "" {
		return 0, 0, false
	}
	i := strings.Index(normText, normQuote)
	if i < 0 {
		return 0, 0, false
	}
	from := utf8.RuneCountInString(normText[:i])
	to := from + utf8.RuneCountInString(normQuote)
	// index maps each normalized rune back to the original rune that produced
	// it; the end bound is one past the original rune of the last matched one.
	return index[from], index[to-1] + 1, true
}

// normalizeForSearch lowercases text and collapses every whitespace run to a
// single space (trimming the edges), returning the normalized string and, per
// normalized rune, the offset of the original rune it came from. Lowercasing
// is per rune, so the mapping stays one-to-one and offsets never drift.
func normalizeForSearch(text string) (string, []int) {
	var b strings.Builder
	b.Grow(len(text))
	index := make([]int, 0, utf8.RuneCountInString(text))
	pendingSpace := false
	pendingAt := 0
	pos := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !pendingSpace {
				pendingSpace = true
				pendingAt = pos
			}
			pos++
			continue
		}
		if pendingSpace && b.Len() > 0 {
			b.WriteRune(' ')
			index = append(index, pendingAt)
		}
		pendingSpace = false
		b.WriteRune(unicode.ToLower(r))
		index = append(index, pos)
		pos++
	}
	return b.String(), index
}
