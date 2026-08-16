package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGateFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate_golden.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadGateGoldenValidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "valid",
			content: `{"band_low":0.35,"band_high":0.75,"cases":[{"text":"a b c d","label":"claim","score":0.9,"llm_gate":true}]}`,
		},
		{
			name:    "no cases",
			content: `{"band_low":0.35,"band_high":0.75,"cases":[]}`,
			wantErr: true,
		},
		{
			name:    "inverted band",
			content: `{"band_low":0.8,"band_high":0.2,"cases":[{"text":"a","label":"claim","score":0.9,"llm_gate":true}]}`,
			wantErr: true,
		},
		{
			name:    "unknown label",
			content: `{"band_low":0.35,"band_high":0.75,"cases":[{"text":"a","label":"maybe","score":0.9,"llm_gate":true}]}`,
			wantErr: true,
		},
		{
			name:    "score out of range",
			content: `{"band_low":0.35,"band_high":0.75,"cases":[{"text":"a","label":"claim","score":1.2,"llm_gate":true}]}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadGateGolden(writeGateFixture(t, tt.content))
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadGateGolden error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// gateFixture builds a fixture whose metrics are known by construction:
// 6 cases outside the band (5 agreeing with the recorded generative verdict,
// 5 correct against the label) and 2 inside it (both resolved by a correct
// generative verdict).
func gateFixture() GateGolden {
	return GateGolden{
		BandLow:  0.3,
		BandHigh: 0.7,
		Cases: []GateCase{
			{Text: "clear claim agreed", Label: "claim", Score: 0.95, LLMGate: true},
			{Text: "clear claim agreed too", Label: "claim", Score: 0.9, LLMGate: true},
			{Text: "clear reject agreed", Label: "not_claim", Score: 0.05, LLMGate: false},
			{Text: "clear reject agreed too", Label: "not_claim", Score: 0.1, LLMGate: false},
			{Text: "local right llm wrong", Label: "claim", Score: 0.85, LLMGate: false},
			{Text: "local wrong llm wrong", Label: "claim", Score: 0.05, LLMGate: false},
			{Text: "band case resolved by llm", Label: "claim", Score: 0.5, LLMGate: true},
			{Text: "band reject resolved by llm", Label: "not_claim", Score: 0.4, LLMGate: false},
		},
	}
}

func TestRunGateRecomputesMetrics(t *testing.T) {
	t.Parallel()
	rep := RunGate(gateFixture())

	if rep.Cases != 8 || rep.OutsideBand != 6 || rep.InBand != 2 {
		t.Fatalf("counts = %d/%d/%d, want 8/6/2", rep.Cases, rep.OutsideBand, rep.InBand)
	}
	if got, want := rep.LLMCallRate, 0.25; got != want {
		t.Errorf("LLMCallRate = %v, want %v", got, want)
	}
	// Outside the band: 5 of 6 local decisions match the label.
	if got, want := rep.OutsideBandAccuracy, 5.0/6.0; got != want {
		t.Errorf("OutsideBandAccuracy = %v, want %v", got, want)
	}
	// Outside the band: 5 of 6 local decisions match the recorded verdict -
	// the only divergence is the case where the local decision is right and
	// the generative one wrong ("local wrong llm wrong" agrees: both reject).
	if got, want := rep.OutsideBandLLMAgreement, 5.0/6.0; got != want {
		t.Errorf("OutsideBandLLMAgreement = %v, want %v", got, want)
	}
	// Cascade: 5 correct outside + 2 correct inside = 7 of 8.
	if got, want := rep.CascadeAccuracy, 7.0/8.0; got != want {
		t.Errorf("CascadeAccuracy = %v, want %v", got, want)
	}
	// Generative gate alone: wrong on two claims = 6 of 8.
	if got, want := rep.LLMAccuracy, 6.0/8.0; got != want {
		t.Errorf("LLMAccuracy = %v, want %v", got, want)
	}
}

func TestRunGateEmptyBandNeverRoutes(t *testing.T) {
	t.Parallel()
	g := gateFixture()
	g.BandLow, g.BandHigh = 0.5, 0.5
	rep := RunGate(g)
	if rep.InBand != 0 {
		t.Errorf("InBand = %d, want 0 with an empty band", rep.InBand)
	}
	if rep.LLMCallRate != 0 {
		t.Errorf("LLMCallRate = %v, want 0", rep.LLMCallRate)
	}
}

func TestCheckGateFloors(t *testing.T) {
	t.Parallel()
	rep := RunGate(gateFixture())

	t.Run("nil gate section skips the check", func(t *testing.T) {
		t.Parallel()
		if failures := (Baseline{}).CheckGate(rep); failures != nil {
			t.Errorf("CheckGate = %v, want nil without a gate baseline", failures)
		}
	})
	t.Run("passing floors", func(t *testing.T) {
		t.Parallel()
		b := Baseline{Gate: &GateBaseline{MinCascadeAccuracy: 0.8, MinOutsideBandLLMAgreement: 0.6, MaxLLMCallRate: 0.3}}
		if failures := b.CheckGate(rep); len(failures) != 0 {
			t.Errorf("CheckGate = %v, want no failures", failures)
		}
	})
	t.Run("each floor can fail", func(t *testing.T) {
		t.Parallel()
		b := Baseline{Gate: &GateBaseline{MinCascadeAccuracy: 0.95, MinOutsideBandLLMAgreement: 0.95, MaxLLMCallRate: 0.1}}
		failures := b.CheckGate(rep)
		if len(failures) != 3 {
			t.Fatalf("CheckGate returned %d failures, want 3: %v", len(failures), failures)
		}
		wantOrder := []string{"cascade-accuracy", "llm-agreement", "llm-call-rate"}
		for i, f := range failures {
			if f.Category != wantOrder[i] {
				t.Errorf("failure %d = %q, want %q", i, f.Category, wantOrder[i])
			}
		}
	})
}
