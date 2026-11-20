package claimreviewsite

import (
	"strings"
	"testing"
	"time"
)

func TestExtractClaimReviewsPlacements(t *testing.T) {
	t.Parallel()
	// A page with a WebPage block, a standalone ClaimReview, and an array block.
	page := `<html><head>
<script type="application/ld+json">{"@type":"WebPage","name":"x"}</script>
<script type="application/ld+json">{"@type":"ClaimReview","claimReviewed":"c1","url":"u1","reviewRating":{"alternateName":"Faux"}}</script>
<script type="application/ld+json">[{"@type":"Organization"},{"@type":"ClaimReview","claimReviewed":"c2","url":"u2","reviewRating":{"alternateName":"Vrai"}}]</script>
<script type="application/ld+json">{"@graph":[{"@type":"ClaimReview","claimReviewed":"c3","url":"u3","reviewRating":{"alternateName":"Vrai"}}]}</script>
</head><body>ignored prose</body></html>`
	got := extractClaimReviews(strings.NewReader(page))
	if len(got) != 3 {
		t.Fatalf("extracted %d ClaimReviews, want 3: %+v", len(got), got)
	}
	claims := map[string]bool{}
	for _, cr := range got {
		claims[cr.ClaimReviewed] = true
	}
	for _, want := range []string{"c1", "c2", "c3"} {
		if !claims[want] {
			t.Errorf("missing claim %q", want)
		}
	}
}

func TestExtractClaimReviewsIgnoresNonLDJSON(t *testing.T) {
	t.Parallel()
	page := `<html><head><script>var x = {"@type":"ClaimReview"};</script></head><body></body></html>`
	if got := extractClaimReviews(strings.NewReader(page)); len(got) != 0 {
		t.Fatalf("extracted %d from a plain <script>, want 0", len(got))
	}
}

func TestParseRobots(t *testing.T) {
	t.Parallel()
	body := `# comment
User-agent: *
Disallow: /admin/
Crawl-delay: 3

User-agent: truth-in-stream-factcheck-bot
Disallow: /private/
`
	r := parseRobots(strings.NewReader(body), "truth-in-stream-factcheck-bot")
	if r.allowed("/private/x") {
		t.Error("UA-specific Disallow not applied")
	}
	if !r.allowed("/articles/1") {
		t.Error("allowed path reported disallowed")
	}
	// The star group applies to a different UA, including its crawl-delay.
	star := parseRobots(strings.NewReader(body), "other-bot")
	if star.allowed("/admin/x") {
		t.Error("star Disallow not applied to other UA")
	}
	if star.crawlDelay != 3*time.Second {
		t.Errorf("crawl-delay = %v, want 3s", star.crawlDelay)
	}
}

func TestParseRobotsEmptyAllowsAll(t *testing.T) {
	t.Parallel()
	r := parseRobots(strings.NewReader(""), "bot")
	if !r.allowed("/anything") {
		t.Error("empty robots should allow everything")
	}
}

func TestRobotsWildcardMatching(t *testing.T) {
	t.Parallel()
	// Patterns of the kind lemonde.fr / francetvinfo.fr actually publish.
	body := "User-agent: *\n" +
		"Disallow: /*/recherche/\n" +
		"Disallow: /*.json$\n" +
		"Disallow: /search\n"
	r := parseRobots(strings.NewReader(body), "bot")
	cases := []struct {
		path    string
		allowed bool
	}{
		{"/les-decodeurs/recherche/x", false}, // /*/recherche/ (wildcard segment)
		{"/a/b/recherche/", false},
		{"/data/feed.json", false}, // /*.json$ (wildcard + end anchor)
		{"/data/feed.jsonx", true}, // '$' anchors: .jsonx is not disallowed
		{"/search?q=1", false},     // prefix match
		{"/searching", false},      // prefix (no anchor) still matches
		{"/articles/2024/x", true}, // nothing disallows it
		{"/les-decodeurs/article", true},
	}
	for _, tc := range cases {
		if got := r.allowed(tc.path); got != tc.allowed {
			t.Errorf("allowed(%q) = %v, want %v", tc.path, got, tc.allowed)
		}
	}
}

// TestRobotsMatcherNoCatastrophicBacktracking guards against a DoS via an
// externally-controlled robots.txt: the adversarial pattern that makes a recursive
// backtracker blow up exponentially must return effectively instantly here. robots.txt
// is fetched from each outlet's live site, so a hostile/misconfigured one must not be
// able to hang the crawler goroutine.
func TestRobotsMatcherNoCatastrophicBacktracking(t *testing.T) {
	t.Parallel()
	// "a*a*...a*b" (20 stars) against a 30-char all-'a' path: the classic catastrophic
	// case. It cannot match (no 'b'), and the greedy matcher must decide that in linear
	// time.
	pattern := strings.Repeat("a*", 20) + "b"
	path := strings.Repeat("a", 30)

	done := make(chan bool, 1)
	go func() { done <- robotPatternMatches(pattern, path) }()
	select {
	case got := <-done:
		if got {
			t.Fatalf("adversarial pattern unexpectedly matched")
		}
	case <-time.After(time.Second):
		t.Fatal("robots matcher did not return within 1s on the adversarial pattern (catastrophic backtracking)")
	}
}

func TestParseSitemapIndexAndUrlset(t *testing.T) {
	t.Parallel()
	index := `<sitemapindex><sitemap><loc> https://x/s1.xml </loc></sitemap><sitemap><loc>https://x/s2.xml</loc></sitemap></sitemapindex>`
	pages, children, err := parseSitemap(strings.NewReader(index))
	if err != nil {
		t.Fatalf("parse index: %v", err)
	}
	if len(pages) != 0 || len(children) != 2 {
		t.Fatalf("index: pages=%d children=%d, want 0/2", len(pages), len(children))
	}
	urlset := `<urlset><url><loc>https://x/a</loc></url><url><loc>https://x/b</loc></url></urlset>`
	p, c, err := parseSitemap(strings.NewReader(urlset))
	if err != nil {
		t.Fatalf("parse urlset: %v", err)
	}
	if len(p) != 2 || len(c) != 0 {
		t.Fatalf("urlset: pages=%d children=%d, want 2/0", len(p), len(c))
	}
}

func TestLdNumberFlexible(t *testing.T) {
	t.Parallel()
	var n ldNumber
	if err := n.UnmarshalJSON([]byte(`"5"`)); err != nil || !n.set || n.val != 5 {
		t.Fatalf("string number: set=%v val=%v err=%v", n.set, n.val, err)
	}
	var m ldNumber
	_ = m.UnmarshalJSON([]byte(`null`))
	if m.set {
		t.Error("null should be unset")
	}
}

func TestLdLicenseStringAndObject(t *testing.T) {
	t.Parallel()
	var s ldLicense
	_ = s.UnmarshalJSON([]byte(`"https://cc/by/4.0"`))
	if s.url != "https://cc/by/4.0" {
		t.Errorf("string license = %q", s.url)
	}
	var o ldLicense
	_ = o.UnmarshalJSON([]byte(`{"@type":"CreativeWork","url":"https://cc/by-nd/4.0"}`))
	if o.url != "https://cc/by-nd/4.0" {
		t.Errorf("object license = %q", o.url)
	}
}
