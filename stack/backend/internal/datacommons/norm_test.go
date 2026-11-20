package datacommons

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// twoCosmeticURLs is a DataFeed whose two records point at the same page spelled
// differently (http+trailing slash vs https) plus one under a restrictive sdLicense.
const twoCosmeticURLs = `{
  "@type":"DataFeed",
  "dataFeedElement":[
    {"item":[{"@type":"ClaimReview","claimReviewed":"c1","datePublished":"2024-01-01","url":"http://factuel.afp.com/article/","author":{"name":"AFP","url":"https://factuel.afp.com/"},"reviewRating":{"alternateName":"Faux"}}]},
    {"item":[{"@type":"ClaimReview","claimReviewed":"c1 again","datePublished":"2024-01-02","url":"https://factuel.afp.com/article","author":{"name":"AFP","url":"https://factuel.afp.com/"},"reviewRating":{"alternateName":"Faux"}}]},
    {"item":[{"@type":"ClaimReview","claimReviewed":"restricted","datePublished":"2024-01-03","url":"https://factuel.afp.com/restricted","author":{"name":"AFP","url":"https://factuel.afp.com/"},"reviewRating":{"alternateName":"Faux"},"sdLicense":"https://creativecommons.org/licenses/by-nd/4.0/"}]}
  ]
}`

func TestFeedDedupCanonicalizesURLAndHonoursLicense(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(twoCosmeticURLs))
	}))
	defer srv.Close()
	c, err := New(Config{
		FeedURL:         srv.URL,
		OutletAllowlist: []string{"factuel.afp.com"},
		MaxPriority:     9,
		HTTPClient:      srv.Client(),
		Retry:           httpx.RetryConfig{MaxRetries: -1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub := &recordingPublisher{}
	stats, err := c.Run(context.Background(), nil, pub)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The by-nd record is skipped; the two cosmetic variants both publish (the
	// worker's upsert collapses them since their canonical IDs match).
	if stats.Published != 2 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, want Published=2 Skipped=1 (by-nd dropped)", stats)
	}
	jobs := pub.jobs(t)
	if jobs[0].ID != "https://factuel.afp.com/article" || jobs[1].ID != jobs[0].ID {
		t.Errorf("dedup key not canonical: %q vs %q", jobs[0].ID, jobs[1].ID)
	}
	if jobs[0].SourceURL != jobs[0].ID {
		t.Errorf("source url not canonical: %q", jobs[0].SourceURL)
	}
}
