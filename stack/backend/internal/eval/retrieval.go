package eval

import (
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Retrieval categories. They name the five failure modes dense embeddings handle
// worst on French political speech, the cases this golden set exists to stress:
// exact numbers, named entities, dates, paraphrase (same fact reworded), and
// near-miss distractors (right entity, wrong number). A retrieval case carries
// exactly one of these so the gate can report recall per category and a
// regression can be traced to the class of query it hurt.
const (
	CategoryNumberPrecision = "number-precision"
	CategoryNamedEntity     = "named-entity"
	CategoryDateAnchored    = "date-anchored"
	CategoryParaphrase      = "paraphrase"
	CategoryNearMiss        = "near-miss"
)

// RetrievalCategories is the closed vocabulary of retrieval-stress categories, in
// a stable order for reporting. A golden case's category must be one of these or
// empty; empty means the case is a verdict-only case that does not participate in
// retrieval scoring.
var RetrievalCategories = []string{
	CategoryNumberPrecision,
	CategoryNamedEntity,
	CategoryDateAnchored,
	CategoryParaphrase,
	CategoryNearMiss,
}

// knownCategories indexes RetrievalCategories for O(1) validation at load time.
var knownCategories = func() map[string]struct{} {
	m := make(map[string]struct{}, len(RetrievalCategories))
	for _, c := range RetrievalCategories {
		m[c] = struct{}{}
	}
	return m
}()

// validCategory reports whether c is empty (a verdict-only case) or one of the
// five retrieval-stress categories.
func validCategory(c string) bool {
	if c == "" {
		return true
	}
	_, ok := knownCategories[c]
	return ok
}

// numericWeight is how much more a token carrying a digit counts than a plain
// word token in the retrieval oracle's similarity. Political claims hinge on the
// exact figure - an unemployment rate, a deficit percentage, a year - so the
// oracle weights numeric tokens up: a passage that shares the claim's exact
// number outranks a same-entity passage carrying a different one, which is the
// discrimination the near-miss category exists to test. A change that drops this
// weighting lets a wrong-number distractor tie the right passage and shows up as
// a recall regression at the gate.
const numericWeight = 3.0

// frenchStopwords are high-frequency French (and a few English) function words
// dropped before scoring so the oracle ranks on content, not on how many times
// two passages both say "de" or "la". The set is deliberately small and closed:
// it is the eval's yardstick, not a production tokenizer, and must stay
// deterministic across runs.
var frenchStopwords = map[string]struct{}{
	"le": {}, "la": {}, "les": {}, "un": {}, "une": {}, "des": {}, "du": {},
	"de": {}, "d": {}, "et": {}, "en": {}, "au": {}, "aux": {}, "a": {},
	"est": {}, "sont": {}, "ce": {}, "cette": {}, "ces": {}, "il": {}, "elle": {},
	"que": {}, "qui": {}, "pour": {}, "par": {}, "sur": {}, "dans": {}, "se": {},
	"son": {}, "sa": {}, "ses": {}, "leur": {}, "leurs": {}, "plus": {}, "the": {},
	"of": {}, "to": {}, "l": {}, "s": {}, "n": {},
}

// fold normalizes a French string for lexical comparison: it strips diacritics
// (NFD decompose then drop combining marks, so "chômage" and "chomage" and any
// accent slip fold together, the accented-French robustness the card calls out)
// and lowercases. It is the single normalization both the claim and every passage
// pass through, so a change here that breaks accent folding is caught by the
// paraphrase and named-entity recall dropping at the gate.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// tokenize folds s and splits it into content tokens. A token is a maximal run
// of letters or digits, except that a comma or dot between two digits is kept
// inside the token so a French decimal ("7,3") or a grouped figure stays one
// numeric token rather than fragmenting, and a percent sign is its own token so
// "7,3%" contributes both the figure and the unit. Stopwords are dropped. The
// result is deterministic and order-preserving.
func tokenize(s string) []string {
	runes := []rune(fold(s))
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tok := cur.String()
		cur.Reset()
		if _, stop := frenchStopwords[tok]; stop {
			return
		}
		tokens = append(tokens, tok)
	}
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur.WriteRune(r)
		case (r == ',' || r == '.') && between(runes, i, unicode.IsDigit):
			cur.WriteRune(r)
		case r == '%':
			flush()
			tokens = append(tokens, "%")
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// between reports whether the runes immediately before and after index i both
// satisfy pred, used to keep a decimal separator inside a numeric token.
func between(runes []rune, i int, pred func(rune) bool) bool {
	return i > 0 && i+1 < len(runes) && pred(runes[i-1]) && pred(runes[i+1])
}

// hasDigit reports whether tok carries a digit, marking it a numeric token that
// scores at numericWeight.
func hasDigit(tok string) bool {
	for _, r := range tok {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// weightedVector turns a token slice into a term -> weight map: each term's
// weight is its occurrence count times numericWeight for numeric terms, 1
// otherwise. It is the sparse vector the oracle's cosine similarity runs over.
func weightedVector(tokens []string) map[string]float64 {
	vec := make(map[string]float64, len(tokens))
	for _, t := range tokens {
		w := 1.0
		if hasDigit(t) {
			w = numericWeight
		}
		vec[t] += w
	}
	return vec
}

// cosine is the cosine similarity of two sparse weighted vectors, in [0, 1] for
// non-negative weights. A zero-norm vector yields 0.
func cosine(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var dot, na, nb float64
	for t, wa := range a {
		na += wa * wa
		if wb, ok := b[t]; ok {
			dot += wa * wb
		}
	}
	for _, wb := range b {
		nb += wb * wb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// scoredPassage is a passage id and its lexical similarity to the query, the unit
// the oracle sorts to produce a ranking.
type scoredPassage struct {
	id    string
	score float64
}

// RankPassages is the retrieval oracle: it ranks a case's candidate passages
// against the claim by the deterministic French-normalized, numeric-weighted
// lexical cosine and returns the passage ids best-first. It is the eval's
// reference retriever - a fixed, offline lexical baseline the golden cases are
// scored against - not production retrieval; when live hybrid retrieval lands the
// real-run procedure compares live recall against the same cases (see README).
// Ties break on passage id ascending so the ranking is stable across runs.
func RankPassages(statement string, passages []Passage) []string {
	query := weightedVector(tokenize(statement))
	scored := make([]scoredPassage, 0, len(passages))
	for _, p := range passages {
		scored = append(scored, scoredPassage{id: p.ID, score: cosine(query, weightedVector(tokenize(p.Text)))})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].id < scored[j].id
	})
	ranked := make([]string, len(scored))
	for i, s := range scored {
		ranked[i] = s.id
	}
	return ranked
}

// recallAtK is the fraction of relevant ids appearing in the top k of ranked. An
// empty relevant set is a programming error the loader rejects, so it returns 0
// here rather than dividing by zero.
func recallAtK(relevant, ranked []string, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	top := ranked
	if k < len(top) {
		top = ranked[:k]
	}
	inTop := make(map[string]struct{}, len(top))
	for _, id := range top {
		inTop[id] = struct{}{}
	}
	hits := 0
	for _, id := range relevant {
		if _, ok := inTop[id]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(relevant))
}

// CategoryRecall is one retrieval category's mean recall over its cases at the
// two reported cut-offs, plus the case count so a reader sees how many claims
// back the number.
type CategoryRecall struct {
	Category  string
	Cases     int
	RecallAt1 float64
	RecallAt3 float64
}

// RetrievalReport is the retrieval oracle's recall over the golden set: a
// per-category breakdown in RetrievalCategories order plus the overall means, the
// numbers the baseline gate asserts against. Total is the count of retrieval
// cases scored (cases carrying a category and a relevant target).
type RetrievalReport struct {
	Total         int
	OverallAt1    float64
	OverallAt3    float64
	ByCategory    []CategoryRecall
	byCategoryIdx map[string]int
}

// Recall returns the per-category recall for category, and whether the report
// carried any case in it.
func (r RetrievalReport) Recall(category string) (CategoryRecall, bool) {
	i, ok := r.byCategoryIdx[category]
	if !ok {
		return CategoryRecall{}, false
	}
	return r.ByCategory[i], true
}

// RunRetrieval scores the retrieval oracle over every retrieval case in the
// golden set (a case with a non-empty category and at least one relevant target),
// aggregating recall@1 and recall@3 overall and per category. Verdict-only cases
// (empty category) are skipped. Categories are reported in RetrievalCategories
// order; a category with no case is omitted from the breakdown.
func RunRetrieval(g Golden) RetrievalReport {
	type acc struct {
		cases  int
		sumAt1 float64
		sumAt3 float64
	}
	perCat := make(map[string]*acc, len(RetrievalCategories))
	var total int
	var sumAt1, sumAt3 float64
	for _, c := range g.Cases {
		if c.Category == "" || len(c.Relevant) == 0 {
			continue
		}
		ranked := RankPassages(c.Statement, c.Passages)
		at1 := recallAtK(c.Relevant, ranked, 1)
		at3 := recallAtK(c.Relevant, ranked, 3)
		total++
		sumAt1 += at1
		sumAt3 += at3
		a := perCat[c.Category]
		if a == nil {
			a = &acc{}
			perCat[c.Category] = a
		}
		a.cases++
		a.sumAt1 += at1
		a.sumAt3 += at3
	}
	rep := RetrievalReport{Total: total, byCategoryIdx: map[string]int{}}
	if total > 0 {
		rep.OverallAt1 = sumAt1 / float64(total)
		rep.OverallAt3 = sumAt3 / float64(total)
	}
	for _, cat := range RetrievalCategories {
		a := perCat[cat]
		if a == nil {
			continue
		}
		rep.byCategoryIdx[cat] = len(rep.ByCategory)
		rep.ByCategory = append(rep.ByCategory, CategoryRecall{
			Category:  cat,
			Cases:     a.cases,
			RecallAt1: a.sumAt1 / float64(a.cases),
			RecallAt3: a.sumAt3 / float64(a.cases),
		})
	}
	return rep
}

// Format renders the retrieval report as a short, stable multi-line summary for
// the eval command and test logs: the overall recall then each category in
// RetrievalCategories order, so two runs diff cleanly.
func (r RetrievalReport) Format() string {
	var b strings.Builder
	b.WriteString("retrieval oracle recall over ")
	b.WriteString(itoa(r.Total))
	b.WriteString(" cases: overall R@1 ")
	b.WriteString(pct(r.OverallAt1))
	b.WriteString(", R@3 ")
	b.WriteString(pct(r.OverallAt3))
	for _, c := range r.ByCategory {
		b.WriteString("\n  ")
		b.WriteString(padRight(c.Category, 16))
		b.WriteString(" R@1 ")
		b.WriteString(pct(c.RecallAt1))
		b.WriteString(" R@3 ")
		b.WriteString(pct(c.RecallAt3))
		b.WriteString(" (")
		b.WriteString(itoa(c.Cases))
		b.WriteString(" cases)")
	}
	return b.String()
}

// pct renders a recall in [0,1] as a fixed one-decimal percentage.
func pct(f float64) string {
	scaled := int(math.Round(f * 1000))
	return itoa(scaled/10) + "." + itoa(scaled%10) + "%"
}

// itoa is a small non-negative integer formatter kept local so the report format
// needs no fmt.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	if neg {
		digits = append(digits, '-')
	}
	slices.Reverse(digits)
	return string(digits)
}

// padRight right-pads s with spaces to width for column-aligned reports.
func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
