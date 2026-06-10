package wiki

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Page is one article entry from a dump stream. RevisionID identifies the
// revision the text belongs to and is the source of truth for the later
// delta sync.
type Page struct {
	ID         int64
	NS         int
	Title      string
	Redirect   bool
	RevisionID int64
	Text       string
}

// StreamRange is the byte span of one independently decompressible bz2
// stream inside the multistream dump. End is exclusive; 0 means the stream
// runs to the end of the file.
type StreamRange struct {
	Start int64
	End   int64
}

// ParseIndex reads a decompressed multistream index ("offset:pageid:title"
// lines; titles may contain colons) and returns the distinct stream ranges in
// file order. Consecutive lines sharing an offset belong to one stream.
func ParseIndex(r io.Reader) ([]StreamRange, error) {
	var ranges []StreamRange

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if text == "" {
			continue
		}
		parts := strings.SplitN(text, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("wiki: index line %d: want offset:pageid:title, got %q", line, text)
		}
		offset, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("wiki: index line %d: parse offset: %w", line, err)
		}
		if n := len(ranges); n > 0 && ranges[n-1].Start == offset {
			continue
		}
		if n := len(ranges); n > 0 {
			if offset < ranges[n-1].Start {
				return nil, fmt.Errorf("wiki: index line %d: offset %d goes backwards", line, offset)
			}
			ranges[n-1].End = offset
		}
		ranges = append(ranges, StreamRange{Start: offset})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("wiki: scan index: %w", err)
	}
	return ranges, nil
}

// DecompressStream returns a reader over the decompressed bytes of one
// stream. Each multistream cluster is a self-contained bz2 stream, so slicing
// the dump at the index offsets decompresses independently; size caps the
// final stream, whose End is 0.
func DecompressStream(ra io.ReaderAt, sr StreamRange, size int64) io.Reader {
	end := sr.End
	if end == 0 {
		end = size
	}
	return bzip2.NewReader(io.NewSectionReader(ra, sr.Start, end-sr.Start))
}

// xmlPage mirrors the dump's <page> element. The redirect marker is the
// presence of the <redirect/> child, which is more reliable than matching
// "#REDIRECT" in the text.
type xmlPage struct {
	Title    string `xml:"title"`
	NS       int    `xml:"ns"`
	ID       int64  `xml:"id"`
	Redirect *struct {
		Title string `xml:"title,attr"`
	} `xml:"redirect"`
	Revision struct {
		ID   int64  `xml:"id"`
		Text string `xml:"text"`
	} `xml:"revision"`
}

// pageOpen/pageClose delimit page elements in the raw stream. Inside the
// dump, literal angle brackets in article text are entity-escaped, so these
// byte sequences only occur as real tags.
const (
	pageOpen  = "<page>"
	pageClose = "</page>"
)

// ParsePages extracts every <page> element from one decompressed stream.
// Streams are XML fragments, not documents - the header stream opens the
// root element and the final stream closes it - so pages are sliced out
// byte-wise and each is decoded on its own.
func ParsePages(r io.Reader) ([]Page, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("wiki: read stream: %w", err)
	}

	pages := make([]Page, 0, bytes.Count(raw, []byte(pageOpen)))
	for len(raw) > 0 {
		start := bytes.Index(raw, []byte(pageOpen))
		if start < 0 {
			break
		}
		rel := bytes.Index(raw[start:], []byte(pageClose))
		if rel < 0 {
			return nil, fmt.Errorf("wiki: page %d: unterminated <page> element", len(pages)+1)
		}
		end := start + rel + len(pageClose)

		var xp xmlPage
		if err := xml.Unmarshal(raw[start:end], &xp); err != nil {
			return nil, fmt.Errorf("wiki: page %d: decode: %w", len(pages)+1, err)
		}
		pages = append(pages, Page{
			ID:         xp.ID,
			NS:         xp.NS,
			Title:      xp.Title,
			Redirect:   xp.Redirect != nil,
			RevisionID: xp.Revision.ID,
			Text:       xp.Revision.Text,
		})
		raw = raw[end:]
	}
	return pages, nil
}
