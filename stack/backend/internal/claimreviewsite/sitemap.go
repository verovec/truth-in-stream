package claimreviewsite

import (
	"encoding/xml"
	"io"
	"strings"
)

// sitemapDoc decodes either a sitemap index (<sitemapindex>) or a URL set
// (<urlset>). Both carry <loc> children; the outer element name distinguishes a
// list of child sitemaps from a list of page URLs.
type sitemapDoc struct {
	XMLName  xml.Name
	Locs     []string `xml:"url>loc"`
	Sitemaps []string `xml:"sitemap>loc"`
}

// parseSitemap decodes a sitemap document, returning the page URLs it lists and the
// child sitemap URLs (for a sitemap index). A caller follows child sitemaps one
// level to reach page URLs. Whitespace around each <loc> is trimmed.
func parseSitemap(r io.Reader) (pageURLs, childSitemaps []string, err error) {
	var doc sitemapDoc
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, nil, err
	}
	for _, u := range doc.Locs {
		if t := strings.TrimSpace(u); t != "" {
			pageURLs = append(pageURLs, t)
		}
	}
	for _, u := range doc.Sitemaps {
		if t := strings.TrimSpace(u); t != "" {
			childSitemaps = append(childSitemaps, t)
		}
	}
	return pageURLs, childSitemaps, nil
}
