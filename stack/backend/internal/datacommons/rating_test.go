package datacommons

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestNormalizeRating(t *testing.T) {
	t.Parallel()
	num := func(s, best, worst string) feedRating {
		var r feedRating
		if s != "" {
			_ = r.RatingValue.UnmarshalJSON([]byte(s))
		}
		if best != "" {
			_ = r.BestRating.UnmarshalJSON([]byte(best))
		}
		if worst != "" {
			_ = r.WorstRating.UnmarshalJSON([]byte(worst))
		}
		return r
	}
	cases := []struct {
		name   string
		rating feedRating
		want   domain.LiteralVerdict
		mapped bool
	}{
		{"textual faux", feedRating{AlternateName: "Faux"}, domain.LiteralInaccurate, true},
		{"textual plutot vrai", feedRating{AlternateName: "Plutôt vrai"}, domain.LiteralAccurate, true},
		{"textual inverifiable", feedRating{AlternateName: "Invérifiable"}, domain.LiteralUnverifiable, true},
		{"numeric low string", num(`"1"`, `"5"`, `"1"`), domain.LiteralInaccurate, true},
		{"numeric high string", num(`"5"`, `"5"`, `"1"`), domain.LiteralAccurate, true},
		{"numeric number type", func() feedRating {
			r := feedRating{}
			_ = r.RatingValue.UnmarshalJSON([]byte(`4`))
			_ = r.BestRating.UnmarshalJSON([]byte(`4`))
			return r
		}(), domain.LiteralAccurate, true},
		{"numeric middle band unmapped", num(`"3"`, `"5"`, `"1"`), domain.LiteralUnverifiable, false},
		{"numeric degenerate scale unmapped", num(`"1"`, `"1"`, `"1"`), domain.LiteralUnverifiable, false},
		{"no signal at all", feedRating{AlternateName: "Non catégorisé"}, domain.LiteralUnverifiable, false},
		{"textual wins over numeric", feedRating{AlternateName: "Faux", RatingValue: mustNum("5"), BestRating: mustNum("5"), WorstRating: mustNum("1")}, domain.LiteralInaccurate, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, mapped := normalizeRating(tc.rating)
			if got != tc.want || mapped != tc.mapped {
				t.Fatalf("normalizeRating = (%q,%v), want (%q,%v)", got, mapped, tc.want, tc.mapped)
			}
		})
	}
}

func mustNum(s string) feedNumber {
	var n feedNumber
	_ = n.UnmarshalJSON([]byte(`"` + s + `"`))
	return n
}

func TestFeedNumberUnmarshal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw string
		set bool
		val float64
	}{
		{`5`, true, 5},
		{`"5"`, true, 5},
		{`1.5`, true, 1.5},
		{`null`, false, 0},
		{`""`, false, 0},
		{`"not a number"`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			var n feedNumber
			if err := json.Unmarshal([]byte(tc.raw), &n); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if n.set != tc.set || (n.set && n.val != tc.val) {
				t.Fatalf("feedNumber(%s) = {set:%v val:%v}, want {set:%v val:%v}", tc.raw, n.set, n.val, tc.set, tc.val)
			}
		})
	}
}

func TestNormalizeDate(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"2019-04-04":                "2019-04-04T00:00:00Z",
		"2019-11-21T00:00:00+00:00": "2019-11-21T00:00:00Z",
		"":                          "",
		"not-a-date":                "",
	}
	for in, want := range cases {
		if got := normalizeDate(in); got != want {
			t.Errorf("normalizeDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeFeedSkipsUnrelatedTopLevelFields(t *testing.T) {
	t.Parallel()
	const doc = `{"@context":"http://schema.org","@type":"DataFeed","dataFeedElement":[` +
		`{"item":[{"claimReviewed":"c1","url":"u1","author":{"name":"O","url":"https://o.fr/"},"reviewRating":{"alternateName":"Faux"}}]}` +
		`],"extra":{"ignored":true}}`
	var got []claimReview
	if err := decodeFeed(strings.NewReader(doc), func(cr claimReview) error {
		got = append(got, cr)
		return nil
	}); err != nil {
		t.Fatalf("decodeFeed: %v", err)
	}
	if len(got) != 1 || got[0].ClaimReviewed != "c1" {
		t.Fatalf("decodeFeed got %+v, want one c1 record", got)
	}
}
