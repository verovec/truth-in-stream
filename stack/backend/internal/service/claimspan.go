package service

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// runeRange is one [start, end) rune-offset interval inside a searched text.
type runeRange struct {
	start int
	end   int
}

// claimSpans anchors one claim's verbatim quote onto the unit's member
// segments: it locates the quote inside text (the members' combined text the
// decomposer read - passed in so it is joined once per unit, not once per
// claim) and projects every matched range onto each member it overlaps, as
// [start, end) rune offsets local to that member's text. When the quoted words
// repeat inside the unit, every occurrence is anchored: the decomposer does not
// say which repetition it meant, and marking all of them is faithful where
// picking one would highlight the wrong row half the time. A quote crossing a
// segment boundary yields one span per segment. An empty or unlocatable quote
// yields nil: the claim is still verified, it just cannot be highlighted, which
// is strictly better than highlighting the wrong words.
func claimSpans(members []unitMember, text, quote string) []domain.ClaimSpan {
	if strings.TrimSpace(quote) == "" {
		return nil
	}
	ranges := locateQuote(text, quote)
	if len(ranges) == 0 {
		return nil
	}
	spans := make([]domain.ClaimSpan, 0, len(ranges))
	for _, r := range ranges {
		// Members sit in the combined rune space at cumulative offsets, each
		// separated from the previous by the single joining space combinedText
		// writes.
		memberStart := 0
		for i, m := range members {
			if i > 0 {
				memberStart++
			}
			memberLen := utf8.RuneCountInString(m.seg.Text)
			spanStart := max(r.start, memberStart)
			spanEnd := min(r.end, memberStart+memberLen)
			if spanStart < spanEnd {
				spans = append(spans, domain.ClaimSpan{
					SegmentID: m.id,
					Start:     spanStart - memberStart,
					End:       spanEnd - memberStart,
				})
			}
			memberStart += memberLen
		}
	}
	if len(spans) == 0 {
		return nil
	}
	return spans
}

// locateQuote finds every non-overlapping occurrence of quote inside text as
// [start, end) rune ranges. It tries exact substring matches first, then a
// tolerant pass that case-folds and collapses whitespace runs on both sides
// (speech-to-text output and a model's copy of it routinely disagree on casing
// and spacing), mapping each normalized hit back onto the original rune
// offsets. An empty result means the quote does not appear even normalized -
// the caller then skips the highlight rather than guessing.
func locateQuote(text, quote string) []runeRange {
	if quote == "" {
		return nil
	}
	if ranges := exactOccurrences(text, quote); len(ranges) > 0 {
		return ranges
	}

	normText, index := normalizeForSearch(text)
	normQuote, _ := normalizeForSearch(quote)
	if normQuote == "" {
		return nil
	}
	var ranges []runeRange
	for from := 0; ; {
		i := strings.Index(normText[from:], normQuote)
		if i < 0 {
			break
		}
		at := from + i
		start := utf8.RuneCountInString(normText[:at])
		end := start + utf8.RuneCountInString(normQuote)
		// index maps each normalized rune back to the original rune that produced
		// it; the end bound is one past the original rune of the last matched one.
		ranges = append(ranges, runeRange{start: index[start], end: index[end-1] + 1})
		from = at + len(normQuote)
	}
	return ranges
}

// exactOccurrences finds every non-overlapping exact occurrence of quote in
// text, as rune ranges.
func exactOccurrences(text, quote string) []runeRange {
	var ranges []runeRange
	for from := 0; ; {
		i := strings.Index(text[from:], quote)
		if i < 0 {
			break
		}
		at := from + i
		start := utf8.RuneCountInString(text[:at])
		ranges = append(ranges, runeRange{start: start, end: start + utf8.RuneCountInString(quote)})
		from = at + len(quote)
	}
	return ranges
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
