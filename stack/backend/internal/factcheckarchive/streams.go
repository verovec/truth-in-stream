package factcheckarchive

import (
	"context"
	"fmt"
	"log/slog"
)

// Strategy declares the broadened French-language ingest surface: a set of topic
// queries walked with languageCode=fr, plus a set of publisher sites each paged in
// full (reviewPublisherSiteFilter, no query term) so an allowlisted outlet's whole
// French catalog is ingested, not only the topic-matched subset. This is what
// takes the Google path from the fixed ~19-topic set to the broadest French yield
// the API exposes. MaxPages and MaxAgeDays apply to every stream.
type Strategy struct {
	Topics         []string
	PublisherSites []string
	MaxPages       int
	MaxAgeDays     int
}

// BuildStreams expands a Strategy into the ordered, de-duplicated list of streams
// to walk: one per topic (a topical query), then one per publisher site (a
// publisher-scoped query with no topic term). Each stream carries a stable
// StreamKey so a checkpoint can record it as drained and a resumed run skips it.
func BuildStreams(s Strategy) []RunConfig {
	streams := make([]RunConfig, 0, len(s.Topics)+len(s.PublisherSites))
	seen := make(map[string]struct{})
	add := func(rc RunConfig) {
		rc.StreamKey = rc.key()
		if _, dup := seen[rc.StreamKey]; dup {
			return
		}
		seen[rc.StreamKey] = struct{}{}
		streams = append(streams, rc)
	}
	for _, topic := range s.Topics {
		if topic == "" {
			continue
		}
		add(RunConfig{Query: topic, MaxPages: s.MaxPages, MaxAgeDays: s.MaxAgeDays})
	}
	for _, site := range s.PublisherSites {
		if site == "" {
			continue
		}
		add(RunConfig{PublisherSite: site, MaxPages: s.MaxPages, MaxAgeDays: s.MaxAgeDays})
	}
	return streams
}

// RunStreams walks every stream, skipping any the checkpoint records as already
// drained in this resumed run, and aggregates their Stats. After each stream
// completes it is marked done and the checkpoint saved, so a crash resumes at the
// next undrained stream rather than re-paging finished ones. On the first stream
// that errors it returns the counts gathered so far and the error, leaving the
// checkpoint holding the completed streams; the caller Clears the checkpoint only
// after every stream has drained. A nil logger falls back to slog.Default.
func (c *Client) RunStreams(ctx context.Context, logger *slog.Logger, pub Publisher, streams []RunConfig, cp StreamCheckpoint) (Stats, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cp == nil {
		cp = NoStreamCheckpoint{}
	}
	var total Stats
	for _, stream := range streams {
		key := stream.key()
		if cp.Done(key) {
			logger.InfoContext(ctx, "skipping already-drained fact-check stream", slog.String("stream", key))
			continue
		}
		stats, err := c.Run(ctx, logger, pub, stream)
		total.Published += stats.Published
		total.Skipped += stats.Skipped
		if err != nil {
			return total, fmt.Errorf("factcheckarchive: stream %q: %w", key, err)
		}
		cp.MarkDone(key)
		if saveErr := cp.Save(); saveErr != nil {
			return total, fmt.Errorf("factcheckarchive: save checkpoint after stream %q: %w", key, saveErr)
		}
	}
	return total, nil
}
