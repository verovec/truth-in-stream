package sdmx

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Source adapts a Client and a curated spec list to the stats.Source contract:
// it fetches every spec and concatenates the datapoints under one institution's
// corpus label, so the source-agnostic stats foundation ingests any SDMX
// institution uniformly. A fetch failure for any spec fails the run (wrapped), so
// a partial corpus is never silently committed; the per-institution isolation the
// producer needs is provided by the producer running each Source separately.
type Source struct {
	client *Client
	corpus string
	specs  []Spec
}

// NewSource builds a Source over client, the evidence corpus label its passages
// are stamped with (one of domain.StatCorpora), and the curated specs. It panics
// on a non-statistical corpus, a programming error the stats.Run guard would also
// catch at runtime but which is cheaper to surface at construction.
func NewSource(client *Client, corpus string, specs []Spec) *Source {
	if !domain.IsStatCorpus(corpus) {
		panic(fmt.Sprintf("sdmx: corpus %q is not a registered statistical corpus", corpus))
	}
	return &Source{client: client, corpus: corpus, specs: specs}
}

// Corpus is the evidence_chunks.source label every passage from this source is
// stamped with, distinct per institution so a retrieved passage's publisher is
// identifiable.
func (s *Source) Corpus() string { return s.corpus }

// Datapoints fetches every curated spec in order and returns the combined
// datapoints. Sequential fetching lets the client's per-endpoint rate limit space
// successive requests.
func (s *Source) Datapoints(ctx context.Context) ([]domain.Datapoint, error) {
	var all []domain.Datapoint
	for _, spec := range s.specs {
		dps, err := s.client.Fetch(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("sdmx: fetch %s/%s: %w", spec.FlowRef, spec.Key, err)
		}
		all = append(all, dps...)
	}
	return all, nil
}
