package nli

import (
	"math"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	valid := Config{ModelPath: "model.onnx", TokenizerPath: "tokenizer.json", Temperature: 1.86, Timeout: time.Second}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "missing model path", mutate: func(c *Config) { c.ModelPath = "" }, wantErr: true},
		{name: "missing tokenizer path", mutate: func(c *Config) { c.TokenizerPath = "" }, wantErr: true},
		{name: "zero temperature", mutate: func(c *Config) { c.Temperature = 0 }, wantErr: true},
		{name: "negative temperature", mutate: func(c *Config) { c.Temperature = -1 }, wantErr: true},
		{name: "nan temperature", mutate: func(c *Config) { c.Temperature = math.NaN() }, wantErr: true},
		{name: "zero timeout", mutate: func(c *Config) { c.Timeout = 0 }, wantErr: true},
		{name: "library path optional", mutate: func(c *Config) { c.LibraryPath = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid
			tt.mutate(&cfg)
			err := cfg.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStanceFromLogits(t *testing.T) {
	t.Parallel()

	t.Run("distribution sums to one and preserves order", func(t *testing.T) {
		t.Parallel()
		s := stanceFromLogits(3, 1, -2, 1)
		total := s.Entailment + s.Neutral + s.Contradiction
		if math.Abs(total-1) > 1e-9 {
			t.Errorf("stance sums to %v, want 1", total)
		}
		if !(s.Entailment > s.Neutral && s.Neutral > s.Contradiction) {
			t.Errorf("stance order not preserved: %+v", s)
		}
	})
	t.Run("temperature softens the winner", func(t *testing.T) {
		t.Parallel()
		sharp := stanceFromLogits(4, 0, 0, 1)
		soft := stanceFromLogits(4, 0, 0, 4)
		if sharp.Entailment <= soft.Entailment {
			t.Errorf("temperature 4 should soften: sharp %v, soft %v", sharp.Entailment, soft.Entailment)
		}
	})
	t.Run("extreme logits stay finite", func(t *testing.T) {
		t.Parallel()
		s := stanceFromLogits(800, -800, 0, 1)
		for _, v := range []float64{s.Entailment, s.Neutral, s.Contradiction} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("non-finite stance component: %+v", s)
			}
		}
	})
}
