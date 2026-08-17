//go:build probe

// This file is the empirical French-coverage probe for the Google Fact Check Tools
// path (card VER-200). It is build-tagged `probe` so it never runs in CI or the
// default suite: it makes live API calls and needs a real key. Run it by hand to
// record the before/after French claim counts:
//
//	FACTCHECK_API_KEY=... FACTCHECK_LEGACY_QUERIES="q1,q2,..." \
//	  go test -tags probe -run TestFrenchCoverageProbe -v ./internal/factcheckarchive/
//
// PROBE_MAX_PAGES bounds pages per stream (default 5) so the probe stays a bounded,
// representative sample rather than a full multi-thousand-request crawl.
package factcheckarchive

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// countingPublisher records the distinct review URLs (the claim IDs) it is asked to
// publish, so the probe measures unique French claims, not raw rows.
type countingPublisher struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (p *countingPublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// The job body carries "id":"<review url>"; a cheap contains-scan avoids importing
	// the job type here. Uniqueness is what we count.
	const marker = `"id":"`
	if i := strings.Index(string(body), marker); i >= 0 {
		rest := string(body)[i+len(marker):]
		if j := strings.Index(rest, `"`); j >= 0 {
			p.ids[rest[:j]] = struct{}{}
		}
	}
	return nil
}

func newProbeClient(t *testing.T, key string) *Client {
	t.Helper()
	c, err := New(Config{APIKey: key, LanguageCode: "fr", MaxPriority: 10})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestFrenchCoverageProbe(t *testing.T) {
	key := os.Getenv("FACTCHECK_API_KEY")
	if key == "" {
		t.Skip("set FACTCHECK_API_KEY to run the live coverage probe")
	}
	maxPages := 5
	if v := os.Getenv("PROBE_MAX_PAGES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxPages = n
		}
	}

	legacy := strings.Split(os.Getenv("FACTCHECK_LEGACY_QUERIES"), ",")
	legacyTopics := make([]string, 0, len(legacy))
	for _, q := range legacy {
		if s := strings.TrimSpace(q); s != "" {
			legacyTopics = append(legacyTopics, s)
		}
	}

	// BEFORE: the fixed legacy topic set, languageCode=fr, no publisher streams.
	before := &countingPublisher{ids: map[string]struct{}{}}
	beforeStreams := BuildStreams(Strategy{Topics: legacyTopics, MaxPages: maxPages})
	if _, err := (&Client{apiKey: key}).probeRun(t, before, beforeStreams); err != nil {
		t.Fatalf("before run: %v", err)
	}

	// AFTER: the broadened topic rotation plus publisher-scoped full-catalogue streams.
	after := &countingPublisher{ids: map[string]struct{}{}}
	afterStreams := BuildStreams(Strategy{
		Topics:         probeDefaultTopics(),
		PublisherSites: probeDefaultSites(),
		MaxPages:       maxPages,
	})
	if _, err := (&Client{apiKey: key}).probeRun(t, after, afterStreams); err != nil {
		t.Fatalf("after run: %v", err)
	}

	t.Logf("FRENCH COVERAGE PROBE (maxPages=%d per stream)", maxPages)
	t.Logf("  BEFORE (legacy %d topics): %d unique French claims", len(legacyTopics), len(before.ids))
	t.Logf("  AFTER  (%d topics + %d publisher streams): %d unique French claims", len(probeDefaultTopics()), len(probeDefaultSites()), len(after.ids))
}

// probeRun walks the streams with a fresh retrying client, so the probe exercises
// the real fetch path (publisher filter, languageCode, pagination).
func (c *Client) probeRun(t *testing.T, pub Publisher, streams []RunConfig) (Stats, error) {
	t.Helper()
	client := newProbeClient(t, c.apiKey)
	return client.RunStreams(context.Background(), nil, pub, streams, NoStreamCheckpoint{})
}

// probeDefaultTopics / probeDefaultSites mirror the config defaults without importing
// internal/config (which would pull in unrelated env parsing); they are only used by
// the probe.
func probeDefaultTopics() []string {
	return []string{
		"élection présidentielle", "élections législatives", "Assemblée nationale",
		"Emmanuel Macron", "Marine Le Pen", "Jean-Luc Mélenchon", "Rassemblement national",
		"retraites", "réforme des retraites", "chômage", "pouvoir d'achat", "inflation",
		"immigration", "sécurité", "impôts", "dette publique", "santé", "école",
		"énergie", "nucléaire", "climat", "logement", "SMIC", "Ukraine", "laïcité",
	}
}

func probeDefaultSites() []string {
	return []string{
		"factuel.afp.com", "lemonde.fr", "francetvinfo.fr",
		"20minutes.fr", "liberation.fr", "observers.france24.com",
	}
}
