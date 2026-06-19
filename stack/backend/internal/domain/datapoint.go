package domain

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// Datapoint is one source-agnostic statistical observation: a single figure for
// a single series at a single period. It is the contract every statistical
// source (the EU SDMX adapter being the first) maps its rows onto, so the
// ingestion foundation can render, embed, and store any source uniformly.
//
// A series is the set of dimension values that, together with the source and
// dataset, identifies what is being measured (e.g. first residence permits,
// total reason, France). Period is when it was measured. Figure/Unit are the
// measurement. The Source* and Dataset fields are provenance: where the number
// came from and how to cite it.
type Datapoint struct {
	// SourceName is the human-readable publisher, e.g. "Eurostat". It appears
	// in the rendered citation.
	SourceName string
	// SourceURL is a resolvable link to the exact figure (the dataset query),
	// stored as the passage's URL so a citation round-trips to the source.
	SourceURL string
	// Dataset is the source's stable dataset code, e.g. "MIGR_RESFIRST". With
	// SeriesKey it forms the provenance key that makes ingestion idempotent.
	Dataset string
	// SeriesKey is the source's stable identifier for this series within the
	// dataset, distinct from every other series the dataset exposes. For SDMX
	// it is the dot-notation dimension key (e.g. "A.TOTAL.TOTAL.TOTAL.PER.FR").
	SeriesKey string
	// Title is a short human label for the series, used as the passage title
	// (e.g. "Premiers titres de séjour délivrés").
	Title string
	// Geography is the geographic area label in French (e.g. "France").
	Geography string
	// Dimensions are the remaining series-distinguishing labels in French, in a
	// stable order, that the rendered sentence weaves in (e.g. ["toutes
	// nationalités", "tous motifs"]). Empty entries are dropped on render.
	Dimensions []string
	// Period is the observation period as the source reports it (e.g. "2022"
	// for annual, "2022-03" for monthly).
	Period string
	// Figure is the observed value.
	Figure float64
	// Unit is the French unit label rendered after the figure (e.g.
	// "personnes", "%").
	Unit string
}

// StatCorpus is the wiki_chunks.corpus label stamped on statistical evidence
// rows. Statistics share the wiki_chunks table and the SearchWiki retrieval
// path with the encyclopedic corpus, but the wiki-maintenance reads (page
// count for the delta-sync denominator, the clustering scan) must exclude them,
// so the label is a shared constant the store can filter on without importing
// the stats service.
const StatCorpus = "eurostat"

// statPageIDMask keeps the FNV-1a hash inside the positive int64 range so the
// derived page id never collides with a negative value and is safe as a bigint
// primary key. Wikipedia page ids are also positive, but the statistical corpus
// is stored separately, so the spaces do not need to be disjoint.
const statPageIDMask = int64(^uint64(0) >> 1)

// SeriesPageID derives a stable, positive int64 page id from the provenance
// (source, dataset, series key) so every period of one series shares it and a
// re-run upserts the same rows. It is the page-id half of the (page_id,
// chunk_index) provenance key the evidence store writes under.
func (d Datapoint) SeriesPageID() int64 {
	h := fnv.New64a()
	// Length-prefix each field so no two distinct field splits hash alike
	// (e.g. dataset "AB"+key "C" must not collide with "A"+"BC").
	for _, part := range []string{d.SourceName, d.Dataset, d.SeriesKey} {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
	}
	// Clear the sign bit so the hash is a positive int64 (a safe bigint PK);
	// masking the uint64 before the conversion makes the result deterministic
	// rather than implementation-defined.
	return int64(h.Sum64() & uint64(statPageIDMask))
}

// PeriodChunkIndex derives a stable, non-negative chunk index from the period
// so one series' periods occupy distinct rows under the same page id. It is the
// chunk-index half of the (page_id, chunk_index) provenance key. Errors on a
// period it cannot map to a small index, so a malformed period fails loudly
// rather than colliding two observations onto one row.
func (d Datapoint) PeriodChunkIndex() (int, error) {
	// Periods are "YYYY" or "YYYY-MM"; encode as year*100+month so two periods
	// never collide and the index stays small and ordered.
	year, month, err := parsePeriod(d.Period)
	if err != nil {
		return 0, err
	}
	return year*100 + month, nil
}

// parsePeriod splits a Eurostat period into a year and a month in [0,12], where
// 0 means an annual period with no month component.
func parsePeriod(period string) (year, month int, err error) {
	parts := strings.SplitN(period, "-", 2)
	year, err = strconv.Atoi(parts[0])
	if err != nil || year < 0 || year > 9999 {
		return 0, 0, fmt.Errorf("domain: datapoint period %q: invalid year", period)
	}
	if len(parts) == 1 {
		return year, 0, nil
	}
	month, err = strconv.Atoi(parts[1])
	if err != nil || month < 1 || month > 12 {
		return 0, 0, fmt.Errorf("domain: datapoint period %q: invalid month", period)
	}
	return year, month, nil
}

// Validate reports the first reason this datapoint cannot be ingested, so bad
// data is rejected before any embedding spend. A datapoint needs a citable
// provenance, a series identity, a period, and a unit.
func (d Datapoint) Validate() error {
	switch {
	case d.SourceName == "":
		return fmt.Errorf("domain: datapoint: empty source name")
	case d.SourceURL == "":
		return fmt.Errorf("domain: datapoint: empty source url")
	case d.Dataset == "":
		return fmt.Errorf("domain: datapoint: empty dataset")
	case d.SeriesKey == "":
		return fmt.Errorf("domain: datapoint: empty series key")
	case d.Title == "":
		return fmt.Errorf("domain: datapoint: empty title")
	case d.Unit == "":
		return fmt.Errorf("domain: datapoint: empty unit")
	}
	if _, err := d.PeriodChunkIndex(); err != nil {
		return err
	}
	return nil
}
