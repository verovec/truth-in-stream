package insee

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Source adapts a Client and a set of specs to the stats.Source contract: it
// fetches every series in order (the Client throttles between requests to honor
// the rate limit) and concatenates the datapoints, so the source-agnostic
// foundation ingests the whole curated INSEE set in one run. A fetch failure for
// any spec fails the run (wrapped), so a partial corpus is never silently
// committed.
type Source struct {
	client *Client
	specs  []Spec
}

// NewSource builds a Source over client and specs. With nil specs it uses
// CuratedSpecs, the default immigrant labor-market set.
func NewSource(client *Client, specs []Spec) *Source {
	if specs == nil {
		specs = CuratedSpecs
	}
	return &Source{client: client, specs: specs}
}

// Corpus is the INSEE statistical corpus label every passage from this source is
// stamped with, distinct from the EU and interior-ministry corpora.
func (s *Source) Corpus() string { return domain.INSEEStatCorpus }

// Datapoints fetches every spec in order and returns the combined datapoints.
func (s *Source) Datapoints(ctx context.Context) ([]domain.Datapoint, error) {
	var all []domain.Datapoint
	for _, spec := range s.specs {
		dps, err := s.client.Fetch(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("insee: fetch %s: %w", spec.IDBank, err)
		}
		all = append(all, dps...)
	}
	return all, nil
}
