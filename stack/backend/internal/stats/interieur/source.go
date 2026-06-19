package interieur

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Source adapts a Client and a set of specs to the stats.Source contract: it
// downloads every resource and concatenates the datapoints, so the
// source-agnostic foundation ingests the whole national open-data set in one
// run. A download or parse failure for any spec fails the run (wrapped), so a
// partial corpus is never silently committed.
type Source struct {
	client *Client
	specs  []Spec
}

// NewSource builds a Source over client and specs. With nil specs it uses
// CuratedSpecs, the default national set covering the motivating claim.
func NewSource(client *Client, specs []Spec) *Source {
	if specs == nil {
		specs = CuratedSpecs
	}
	return &Source{client: client, specs: specs}
}

// Corpus is the national-source statistical corpus label every interior-ministry
// passage is stamped with, distinct from the EU and INSEE corpora.
func (s *Source) Corpus() string { return domain.InteriorStatCorpus }

// Datapoints downloads every spec in order and returns the combined datapoints.
func (s *Source) Datapoints(ctx context.Context) ([]domain.Datapoint, error) {
	var all []domain.Datapoint
	for _, spec := range s.specs {
		dps, err := s.client.Fetch(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("interieur: fetch %s: %w", spec.Dataset, err)
		}
		all = append(all, dps...)
	}
	return all, nil
}
