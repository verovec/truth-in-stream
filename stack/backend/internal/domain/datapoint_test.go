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
