package eurostat

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Source adapts a Client and a set of specs to the stats.Source contract: it
// fetches every spec and concatenates the datapoints, so the source-agnostic
// foundation ingests the whole curated EU set in one run. A fetch failure for
// any spec fails the run (wrapped), so a partial corpus is never silently
// committed.
type Source struct {
	client *Client
	specs  []Spec
}

// NewSource builds a Source over client and specs. With nil specs it uses
// CuratedSpecs, the default EU set covering the motivating claim.
func NewSource(client *Client, specs []Spec) *Source {
	if specs == nil {
		specs = CuratedSpecs
	}
	return &Source{client: client, specs: specs}
}

// Datapoints fetches every spec in order and returns the combined datapoints.
func (s *Source) Datapoints(ctx context.Context) ([]domain.Datapoint, error) {
	var all []domain.Datapoint
	for _, spec := range s.specs {
		dps, err := s.client.Fetch(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("eurostat: fetch %s/%s: %w", spec.Dataset, spec.Key, err)
		}
		all = append(all, dps...)
	}
	return all, nil
}
