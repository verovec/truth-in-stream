package domain

import "testing"

func baseDatapoint() Datapoint {
	return Datapoint{
		SourceName: "Eurostat",
		SourceURL:  "https://ec.europa.eu/eurostat/api/dissemination/sdmx/2.1/data/MIGR_RESFIRST/A.TOTAL.TOTAL.TOTAL.PER.FR?format=SDMX-CSV",
		Dataset:    "MIGR_RESFIRST",
		SeriesKey:  "A.TOTAL.TOTAL.TOTAL.PER.FR",
		Title:      "Premiers titres de séjour délivrés",
		Geography:  "France",
		Dimensions: []string{"toutes nationalités", "tous motifs"},
		Period:     "2022",
		Figure:     326948,
		Unit:       "personnes",
	}
}

func TestSeriesPageIDStableAndPositive(t *testing.T) {
	d := baseDatapoint()
	first := d.SeriesPageID()
	if first <= 0 {
		t.Fatalf("SeriesPageID = %d, want positive", first)
	}
	// Stable across calls and across the period changing: a series shares its
	// page id over all its periods.
	d.Period = "2021"
	d.Figure = 287179
	if got := d.SeriesPageID(); got != first {
		t.Errorf("SeriesPageID changed with period: %d != %d", got, first)
	}
}

func TestSeriesPageIDDistinctSeries(t *testing.T) {
	a := baseDatapoint()
	b := baseDatapoint()
	b.SeriesKey = "A.PC.T.Y15-64.FOR.FR"
	b.Dataset = "LFSA_ARGAN"
	if a.SeriesPageID() == b.SeriesPageID() {
		t.Errorf("distinct series share page id %d", a.SeriesPageID())
	}
}

func TestSeriesPageIDNoFieldSplitCollision(t *testing.T) {
	a := baseDatapoint()
	a.Dataset = "AB"
	a.SeriesKey = "C"
	b := baseDatapoint()
	b.Dataset = "A"
	b.SeriesKey = "BC"
	if a.SeriesPageID() == b.SeriesPageID() {
		t.Errorf("field-split collision: %q+%q hashed same as %q+%q", a.Dataset, a.SeriesKey, b.Dataset, b.SeriesKey)
	}
}

func TestPeriodChunkIndex(t *testing.T) {
	tests := []struct {
		period  string
		want    int
		wantErr bool
	}{
		{"2022", 202200, false},
		{"2021", 202100, false},
		{"2022-03", 202203, false},
		{"2022-12", 202212, false},
		{"", 0, true},
		{"20x2", 0, true},
		{"2022-13", 0, true},
		{"2022-00", 0, true},
		{"2022-3", 202203, false},
		// INSEE BDM quarterly periods are "YYYY-Qn"; they map into a slot range
		// (21..24) disjoint from the 1..12 months so a quarter never collides with
		// a month and the four quarters sort within the year.
		{"2022-Q1", 202221, false},
		{"2022-Q2", 202222, false},
		{"2022-Q3", 202223, false},
		{"2022-Q4", 202224, false},
		{"2022-Q0", 0, true},
		{"2022-Q5", 0, true},
		{"2022-Qx", 0, true},
		// Year 0 is not a real statistical year; reject it so a base-period
		// sentinel never lands on chunk index 0 alongside real annual rows.
		{"0", 0, true},
		{"-2022", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.period, func(t *testing.T) {
			d := baseDatapoint()
			d.Period = tt.period
			got, err := d.PeriodChunkIndex()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("PeriodChunkIndex(%q) = %d, want error", tt.period, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PeriodChunkIndex(%q): unexpected error %v", tt.period, err)
			}
			if got != tt.want {
				t.Errorf("PeriodChunkIndex(%q) = %d, want %d", tt.period, got, tt.want)
			}
		})
	}
}

func TestPeriodChunkIndexDistinctPeriods(t *testing.T) {
	d := baseDatapoint()
	d.Period = "2021"
	a, _ := d.PeriodChunkIndex()
	d.Period = "2022"
	b, _ := d.PeriodChunkIndex()
	if a == b {
		t.Errorf("distinct periods share chunk index %d", a)
	}
	if b <= a {
		t.Errorf("later period %d should sort after earlier %d", b, a)
	}
}

// TestPeriodChunkIndexQuarterlyDisjointFromMonthly proves a quarter slot never
// collides with a real month in the same year and that the four quarters sort
// after the months, so a series that mixes frequencies (it never does, but the
// key space must still be unambiguous) never strands two observations on one row.
func TestPeriodChunkIndexQuarterlyDisjointFromMonthly(t *testing.T) {
	d := baseDatapoint()
	seen := map[int]string{}
	periods := []string{
		"2022-01", "2022-02", "2022-03", "2022-04", "2022-05", "2022-06",
		"2022-07", "2022-08", "2022-09", "2022-10", "2022-11", "2022-12",
		"2022-Q1", "2022-Q2", "2022-Q3", "2022-Q4", "2022",
	}
	for _, p := range periods {
		d.Period = p
		idx, err := d.PeriodChunkIndex()
		if err != nil {
			t.Fatalf("PeriodChunkIndex(%q): %v", p, err)
		}
		if other, dup := seen[idx]; dup {
			t.Fatalf("period %q and %q collide on chunk index %d", p, other, idx)
		}
		seen[idx] = p
	}
	d.Period = "2022-Q1"
	q1, _ := d.PeriodChunkIndex()
	d.Period = "2022-Q2"
	q2, _ := d.PeriodChunkIndex()
	if q2 <= q1 {
		t.Errorf("Q2 chunk index %d should sort after Q1 %d", q2, q1)
	}
}

// TestStatCorporaIncludesINSEEThemes proves every INSEE economic-theme corpus is
// a registered statistical corpus, so the wiki-only maintenance reads exclude it
// exactly like the other statistical corpora and a macro theme never skews the
// encyclopedic page-count guard or the clustering scan.
func TestStatCorporaIncludesINSEEThemes(t *testing.T) {
	themes := []string{
		INSEEStatCorpus,
		INSEEUnemploymentCorpus,
		INSEEEmploymentCorpus,
		INSEEPricesCorpus,
		INSEEGDPCorpus,
	}
	registered := map[string]bool{}
	for _, c := range StatCorpora() {
		registered[c] = true
	}
	for _, theme := range themes {
		if theme == "" {
			t.Errorf("theme corpus label is empty")
		}
		if !registered[theme] {
			t.Errorf("corpus %q not in StatCorpora exclusion set", theme)
		}
		if !IsStatCorpus(theme) {
			t.Errorf("IsStatCorpus(%q) = false, want true", theme)
		}
	}
	// The labels must be distinct so a retrieved passage's theme is identifiable.
	seen := map[string]bool{}
	for _, theme := range themes {
		if seen[theme] {
			t.Errorf("duplicate corpus label %q", theme)
		}
		seen[theme] = true
	}
}

func TestDatapointValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Datapoint)
		wantErr bool
	}{
		{"valid", func(*Datapoint) {}, false},
		{"empty source name", func(d *Datapoint) { d.SourceName = "" }, true},
		{"empty source url", func(d *Datapoint) { d.SourceURL = "" }, true},
		{"empty dataset", func(d *Datapoint) { d.Dataset = "" }, true},
		{"empty series key", func(d *Datapoint) { d.SeriesKey = "" }, true},
		{"empty title", func(d *Datapoint) { d.Title = "" }, true},
		{"empty unit", func(d *Datapoint) { d.Unit = "" }, true},
		{"bad period", func(d *Datapoint) { d.Period = "nope" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := baseDatapoint()
			tt.mutate(&d)
			err := d.Validate()
			if tt.wantErr != (err != nil) {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
