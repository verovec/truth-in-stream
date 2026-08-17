package claimreviewsite

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// claimReview is the subset of a schema.org ClaimReview node this reader keeps:
// claim text, rating, review URL, publication date, author/outlet, and the
// structured-data licence. It deliberately carries NO article body: the reader
// extracts only these categorical fields from the page's JSON-LD, never the prose.
type claimReview struct {
	ClaimReviewed string    `json:"claimReviewed"`
	DatePublished string    `json:"datePublished"`
	URL           string    `json:"url"`
	Author        ldOrg     `json:"author"`
	Publisher     ldOrg     `json:"publisher"`
	ReviewRating  ldRating  `json:"reviewRating"`
	SdLicense     ldLicense `json:"sdLicense"`
}

type ldOrg struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type ldRating struct {
	AlternateName string   `json:"alternateName"`
	RatingValue   ldNumber `json:"ratingValue"`
	BestRating    ldNumber `json:"bestRating"`
	WorstRating   ldNumber `json:"worstRating"`
}

// ldNumber decodes a schema.org numeric field a publisher may serialize as a JSON
// number or a quoted string; an absent, null, or unparseable value leaves it unset
// rather than failing the whole page decode.
type ldNumber struct {
	set bool
	val float64
}

func (n *ldNumber) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	n.val, n.set = f, true
	return nil
}

// ldLicense decodes sdLicense, which schema.org allows as either a bare URL string
// or a CreativeWork/URL object carrying @id or url. Only the license URL is kept.
type ldLicense struct {
	url string
}

func (l *ldLicense) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err == nil {
			l.url = s
		}
		return nil
	}
	var obj struct {
		ID  string `json:"@id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &obj); err == nil {
		if obj.URL != "" {
			l.url = obj.URL
		} else {
			l.url = obj.ID
		}
	}
	return nil
}

// extractClaimReviews pulls every schema.org ClaimReview node from a page's
// application/ld+json script blocks. It handles the three valid placements: a
// standalone object, an array of objects, and a node nested in an @graph array. It
// never buffers or returns the article body: only the JSON-LD blocks are parsed.
func extractClaimReviews(r io.Reader) []claimReview {
	blocks := ldJSONBlocks(r)
	out := make([]claimReview, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, claimReviewsFromBlock([]byte(block))...)
	}
	return out
}

// ldJSONBlocks returns the text content of every <script type="application/ld+json">
// element, using the HTML tokenizer so malformed surrounding markup does not break
// extraction.
func ldJSONBlocks(r io.Reader) []string {
	var blocks []string
	z := html.NewTokenizer(r)
	for {
		switch z.Next() {
		case html.ErrorToken:
			return blocks
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "script" || !hasAttr {
				continue
			}
			if !isLDJSON(z) {
				continue
			}
			if z.Next() == html.TextToken {
				blocks = append(blocks, string(z.Text()))
			}
		}
	}
}

// isLDJSON reports whether the script tag the tokenizer is on declares
// type="application/ld+json".
func isLDJSON(z *html.Tokenizer) bool {
	for {
		key, val, more := z.TagAttr()
		if string(key) == "type" && strings.EqualFold(strings.TrimSpace(string(val)), "application/ld+json") {
			return true
		}
		if !more {
			return false
		}
	}
}

// claimReviewsFromBlock decodes one ld+json block and returns every ClaimReview it
// contains, whether the block is a single node, an array, or an @graph wrapper.
func claimReviewsFromBlock(block []byte) []claimReview {
	block = []byte(strings.TrimSpace(string(block)))
	if len(block) == 0 {
		return nil
	}
	switch block[0] {
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(block, &arr); err != nil {
			return nil
		}
		var out []claimReview
		for _, n := range arr {
			out = append(out, claimReviewsFromNode(n)...)
		}
		return out
	case '{':
		return claimReviewsFromNode(block)
	default:
		return nil
	}
}

// claimReviewsFromNode returns the ClaimReview(s) in one JSON-LD object: the object
// itself when its @type is ClaimReview, plus any ClaimReview members of an @graph.
func claimReviewsFromNode(node json.RawMessage) []claimReview {
	var typed struct {
		Type  ldType            `json:"@type"`
		Graph []json.RawMessage `json:"@graph"`
	}
	if err := json.Unmarshal(node, &typed); err != nil {
		return nil
	}
	out := make([]claimReview, 0, 1+len(typed.Graph))
	if typed.Type.has("ClaimReview") {
		var cr claimReview
		if err := json.Unmarshal(node, &cr); err == nil {
			out = append(out, cr)
		}
	}
	for _, g := range typed.Graph {
		out = append(out, claimReviewsFromNode(g)...)
	}
	return out
}

// ldType decodes an @type that may be a single string or an array of strings.
type ldType []string

func (t *ldType) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 {
		return nil
	}
	if b[0] == '[' {
		return json.Unmarshal(b, (*[]string)(t))
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return nil
	}
	*t = []string{s}
	return nil
}

func (t ldType) has(want string) bool {
	for _, s := range t {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
