package wiki

import (
	"strings"
	"testing"
)

func TestExtractLead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		wikitext string
		want     string
	}{
		{
			name:     "plain paragraph unchanged",
			wikitext: "Paris is the capital of France.",
			want:     "Paris is the capital of France.",
		},
		{
			name:     "cuts at first heading",
			wikitext: "Lead paragraph.\n\n== History ==\nBody text.",
			want:     "Lead paragraph.",
		},
		{
			name:     "cuts at deeper heading",
			wikitext: "Lead paragraph.\n\n=== Sub ===\nBody.",
			want:     "Lead paragraph.",
		},
		{
			name:     "heading requires line start",
			wikitext: "An equation a == b holds.\n\n== Later ==\nBody.",
			want:     "An equation a == b holds.",
		},
		{
			name:     "internal link keeps label",
			wikitext: "[[France]] borders [[Kingdom of Spain|Spain]].",
			want:     "France borders Spain.",
		},
		{
			name:     "bold and italics stripped",
			wikitext: "'''Paris''' is ''very'' old.",
			want:     "Paris is very old.",
		},
		{
			name:     "templates removed including nested",
			wikitext: "{{Infobox city|name=Paris|pop={{formatnum:2000000}}}}Paris is a city.",
			want:     "Paris is a city.",
		},
		{
			name:     "references removed",
			wikitext: "Paris is old.<ref>Some source</ref> It is big.<ref name=\"x\"/>",
			want:     "Paris is old. It is big.",
		},
		{
			name:     "self-closing ref with slash in attribute keeps following prose",
			wikitext: "Einstein<ref name=\"nyt/1921\"/> won the Nobel Prize in 1921.<ref>{{cite web}}</ref> He was famous.",
			want:     "Einstein won the Nobel Prize in 1921. He was famous.",
		},
		{
			name:     "references tag is not mistaken for a ref pair",
			wikitext: "Paris is old.<references/> It is big.",
			want:     "Paris is old. It is big.",
		},
		{
			name:     "multi-pipe link keeps everything after the first pipe",
			wikitext: "See [[Help:Foo|a|b]] now.",
			want:     "See a|b now.",
		},
		{
			name:     "nested link inside a label resolves to its own label",
			wikitext: "[[Help:Foo|see [[Foo|bar]] here]] please.",
			want:     "see bar here please.",
		},
		{
			name:     "html comments removed",
			wikitext: "Paris<!-- citation needed --> is old.",
			want:     "Paris is old.",
		},
		{
			name:     "external link keeps label",
			wikitext: "See [https://example.org the site] for more.",
			want:     "See the site for more.",
		},
		{
			name:     "bare external link removed",
			wikitext: "See [https://example.org] for more.",
			want:     "See for more.",
		},
		{
			name:     "file and category links removed",
			wikitext: "[[File:Paris.jpg|thumb|A view of [[Paris]]]][[Category:Capitals]]Paris is a city.",
			want:     "Paris is a city.",
		},
		{
			name:     "html tags stripped",
			wikitext: "Paris is <small>quite</small> old.",
			want:     "Paris is quite old.",
		},
		{
			name:     "blank lines collapse paragph breaks",
			wikitext: "First paragraph.\n\n\n\nSecond paragraph.",
			want:     "First paragraph.\n\nSecond paragraph.",
		},
		{
			name:     "empty after stripping",
			wikitext: "{{stub}}",
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractLead(tc.wikitext)
			if got != tc.want {
				t.Errorf("ExtractLead(%q) = %q, want %q", tc.wikitext, got, tc.want)
			}
		})
	}
}

func TestExtractLeadLongArticle(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("Lead sentence one. Lead sentence two.\n\nSecond lead paragraph.\n\n")
	b.WriteString("== History ==\n")
	for range 100 {
		b.WriteString("Body filler that must not appear in the lead.\n")
	}

	got := ExtractLead(b.String())
	want := "Lead sentence one. Lead sentence two.\n\nSecond lead paragraph."
	if got != want {
		t.Errorf("ExtractLead() = %q, want %q", got, want)
	}
}

func TestIsDisambiguation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		wikitext string
		want     bool
	}{
		{name: "plain article", wikitext: "Paris is the capital of France.", want: false},
		{name: "disambiguation template", wikitext: "'''Mercury''' may refer to:\n{{disambiguation}}", want: true},
		{name: "disambig template", wikitext: "{{disambig}}", want: true},
		{name: "dab template", wikitext: "{{dab}}", want: true},
		{name: "hndis template", wikitext: "{{hndis|name=Smith, John}}", want: true},
		{name: "case insensitive", wikitext: "{{Disambiguation}}", want: true},
		{name: "with argument", wikitext: "{{disambiguation|geo}}", want: true},
		{name: "geodis", wikitext: "{{geodis}}", want: true},
		{name: "dis alias", wikitext: "{{dis}}", want: true},
		{name: "dis does not match other templates", wikitext: "{{display title|Foo}}", want: false},
		{name: "spelled out variant", wikitext: "{{music disambiguation}}", want: true},
		{name: "disambig magic word", wikitext: "Some text\n__DISAMBIG__", want: true},
		{name: "surname is not disambiguation", wikitext: "{{surname}}", want: false},
		{name: "mention in prose is not a template", wikitext: "This page is not a disambiguation page.", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsDisambiguation(tc.wikitext); got != tc.want {
				t.Errorf("IsDisambiguation(%q) = %v, want %v", tc.wikitext, got, tc.want)
			}
		})
	}
}
