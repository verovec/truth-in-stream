package speakername

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// token is one word of a transcript turn: its accent-folded lowercase form for
// matching, its byte range in the original text for verbatim quoting, and
// whether it was capitalized as heard.
type token struct {
	norm       string
	start, end int
	upper      bool
}

// tokenize splits text into letter-and-digit words. Apostrophes and hyphens
// separate words, so "d'Arnaud" and "Jean-Luc" each yield two tokens and a name
// matches however the transcriber punctuated it.
func tokenize(text string) []token {
	var tokens []token
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		word := text[start:end]
		first, _ := firstRune(word)
		tokens = append(tokens, token{norm: fold(word), start: start, end: end, upper: unicode.IsUpper(first)})
		start = -1
	}
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(text))
	return tokens
}

func firstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

// fold lowercases s and drops its combining marks, so "Éric" and "eric" match.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// foldedWords returns the folded words of s, the form names are compared in.
func foldedWords(s string) []string {
	tokens := tokenize(s)
	words := make([]string, len(tokens))
	for i, t := range tokens {
		words[i] = t.norm
	}
	return words
}

// excerpt returns the verbatim text around the tokens [from, to), widened by
// excerptContextWords on each side and cut on word boundaries, so a quoted span
// is always an exact substring of the turn it came from.
func excerpt(text string, tokens []token, from, to int) string {
	first := max(0, from-excerptContextWords)
	last := min(len(tokens), to+excerptContextWords) - 1
	return text[tokens[first].start:tokens[last].end]
}

// hasWordsAt reports whether words appear contiguously in tokens starting at i.
func hasWordsAt(tokens []token, i int, words []string) bool {
	if i < 0 || i+len(words) > len(tokens) {
		return false
	}
	for k, w := range words {
		if tokens[i+k].norm != w {
			return false
		}
	}
	return true
}
