package domain

import (
	"testing"
	"time"
)

func TestPeriodStart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		period string
		want   time.Time
	}{
		{"2022", time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2022-03", time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"2022-Q1", time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2022-Q4", time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		got, err := Datapoint{Period: tc.period}.PeriodStart()
		if err != nil {
			t.Errorf("PeriodStart(%q): %v", tc.period, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("PeriodStart(%q) = %v, want %v", tc.period, got, tc.want)
		}
	}
	if _, err := (Datapoint{Period: "not-a-period"}).PeriodStart(); err == nil {
		t.Error("PeriodStart on invalid period returned nil error")
	}
}
