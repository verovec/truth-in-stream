package wiki

import (
	"compress/bzip2"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		index   string
		want    []StreamRange
		wantErr bool
	}{
		{
			name: "groups pages by stream offset",
			index: "100:1:Paris\n" +
				"100:2:France\n" +
				"250:3:Mercury\n" +
				"250:4:Talk:Something\n",
			want: []StreamRange{{Start: 100, End: 250}, {Start: 250, End: 0}},
		},
		{
			name:  "title containing colons",
			index: "100:1:C:\\Windows: a history\n",
			want:  []StreamRange{{Start: 100, End: 0}},
		},
		{
			name:  "trailing newline and blank lines ignored",
			index: "100:1:Paris\n\n\n",
			want:  []StreamRange{{Start: 100, End: 0}},
		},
		{
			name:    "malformed line",
			index:   "not-an-offset:1:Paris\n",
			wantErr: true,
		},
		{
			name:    "missing fields",
			index:   "100:1\n",
			wantErr: true,
		},
		{
			name:  "empty index",
			index: "",
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseIndex(strings.NewReader(tc.index))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseIndex(%q) succeeded, want error", tc.index)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIndex(%q): %v", tc.index, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseIndex(%q) mismatch (-want +got):\n%s", tc.index, diff)
			}
		})
	}
}

const pageXML = `
  <page>
    <title>Paris</title>
    <ns>0</ns>
    <id>1</id>
    <revision>
      <id>100</id>
      <timestamp>2026-01-15T10:30:00Z</timestamp>
      <text bytes="44" xml:space="preserve">'''Paris''' is the capital of [[France]].</text>
    </revision>
  </page>
  <page>
    <title>The City of Light</title>
    <ns>0</ns>
    <id>2</id>
    <redirect title="Paris" />
    <revision>
      <id>101</id>
      <text>#REDIRECT [[Paris]]</text>
    </revision>
  </page>
  <page>
    <title>Talk:Paris</title>
    <ns>1</ns>
    <id>3</id>
    <revision>
      <id>102</id>
      <text>Discussion.</text>
    </revision>
  </page>
`

func TestParsePages(t *testing.T) {
	t.Parallel()

	got, err := ParsePages(strings.NewReader(pageXML))
	if err != nil {
		t.Fatalf("ParsePages: %v", err)
	}

	want := []Page{
		{ID: 1, NS: 0, Title: "Paris", RevisionID: 100, Text: "'''Paris''' is the capital of [[France]]."},
		{ID: 2, NS: 0, Title: "The City of Light", Redirect: true, RevisionID: 101, Text: "#REDIRECT [[Paris]]"},
		{ID: 3, NS: 1, Title: "Talk:Paris", RevisionID: 102, Text: "Discussion."},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ParsePages mismatch (-want +got):\n%s", diff)
	}
}

func TestParsePagesToleratesUnbalancedWrapper(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		xml   string
		count int
	}{
		{
			name: "header stream with unclosed mediawiki root",
			xml: `<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/" version="0.11">
  <siteinfo><sitename>Wikipedia</sitename></siteinfo>
  <page><title>A</title><ns>0</ns><id>1</id><revision><id>10</id><text>a</text></revision></page>`,
			count: 1,
		},
		{
			name:  "final stream with dangling close tag",
			xml:   `<page><title>B</title><ns>0</ns><id>2</id><revision><id>20</id><text>b</text></revision></page></mediawiki>`,
			count: 1,
		},
		{
			name:  "header-only stream has no pages",
			xml:   `<mediawiki version="0.11"><siteinfo><sitename>Wikipedia</sitename></siteinfo>`,
			count: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePages(strings.NewReader(tc.xml))
			if err != nil {
				t.Fatalf("ParsePages: %v", err)
			}
			if len(got) != tc.count {
				t.Errorf("ParsePages returned %d pages, want %d", len(got), tc.count)
			}
		})
	}
}

// TestFixtureRoundTrip exercises the real multistream path: a committed bz2
// dump fixture (three independently compressed streams: header, two page
// clusters) and its bz2 index, sliced by ParseIndex offsets and decompressed
// stream by stream.
func TestFixtureRoundTrip(t *testing.T) {
	t.Parallel()

	idxFile, err := os.Open("testdata/fixture-multistream-index.txt.bz2")
	if err != nil {
		t.Fatalf("open index fixture: %v", err)
	}
	defer func() { _ = idxFile.Close() }()

	ranges, err := ParseIndex(bzip2.NewReader(idxFile))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(ranges) != 2 {
		t.Fatalf("got %d stream ranges, want 2", len(ranges))
	}

	dump, err := os.Open("testdata/fixture-multistream.xml.bz2")
	if err != nil {
		t.Fatalf("open dump fixture: %v", err)
	}
	defer func() { _ = dump.Close() }()
	info, err := dump.Stat()
	if err != nil {
		t.Fatalf("stat dump fixture: %v", err)
	}

	var all []Page
	for _, sr := range ranges {
		pages, err := ParsePages(DecompressStream(dump, sr, info.Size()))
		if err != nil {
			t.Fatalf("stream at %d: %v", sr.Start, err)
		}
		all = append(all, pages...)
	}

	wantTitles := []string{"Paris", "The City of Light", "Mercury", "Talk:Paris"}
	gotTitles := make([]string, len(all))
	for i, p := range all {
		gotTitles[i] = p.Title
	}
	if diff := cmp.Diff(wantTitles, gotTitles); diff != "" {
		t.Errorf("fixture page titles mismatch (-want +got):\n%s", diff)
	}

	for _, p := range all {
		if p.ID == 0 || p.RevisionID == 0 {
			t.Errorf("page %q missing ids: %+v", p.Title, p)
		}
	}
	if !all[1].Redirect {
		t.Errorf("page %q should be a redirect", all[1].Title)
	}
	if all[3].NS != 1 {
		t.Errorf("page %q ns = %d, want 1", all[3].Title, all[3].NS)
	}
	if !strings.Contains(all[0].Text, "capital of") {
		t.Errorf("page %q text not preserved: %q", all[0].Title, all[0].Text)
	}
}

// TestDecompressStreamBoundaries proves a stream decompresses independently:
// reading the second range must not require bytes from the first.
func TestDecompressStreamBoundaries(t *testing.T) {
	t.Parallel()

	idxFile, err := os.Open("testdata/fixture-multistream-index.txt.bz2")
	if err != nil {
		t.Fatalf("open index fixture: %v", err)
	}
	defer func() { _ = idxFile.Close() }()
	ranges, err := ParseIndex(bzip2.NewReader(idxFile))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}

	dump, err := os.Open("testdata/fixture-multistream.xml.bz2")
	if err != nil {
		t.Fatalf("open dump fixture: %v", err)
	}
	defer func() { _ = dump.Close() }()
	info, err := dump.Stat()
	if err != nil {
		t.Fatalf("stat dump fixture: %v", err)
	}

	last := ranges[len(ranges)-1]
	raw, err := io.ReadAll(DecompressStream(dump, last, info.Size()))
	if err != nil {
		t.Fatalf("decompress last stream: %v", err)
	}
	if !strings.Contains(string(raw), "Mercury") {
		t.Errorf("last stream does not contain expected page: %q", raw)
	}
	if strings.Contains(string(raw), "Paris is the capital") {
		t.Errorf("last stream leaked content from an earlier stream")
	}
}
