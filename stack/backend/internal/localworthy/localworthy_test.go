package localworthy

import (
	"math"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	valid := Config{ModelPath: "model.onnx", TokenizerPath: "tokenizer.json", Timeout: 100 * time.Millisecond}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "missing model path", mutate: func(c *Config) { c.ModelPath = "" }, wantErr: true},
		{name: "missing tokenizer path", mutate: func(c *Config) { c.TokenizerPath = "" }, wantErr: true},
		{name: "zero timeout", mutate: func(c *Config) { c.Timeout = 0 }, wantErr: true},
		{name: "negative timeout", mutate: func(c *Config) { c.Timeout = -time.Second }, wantErr: true},
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

func TestPositiveProbability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		negative float32
		positive float32
		want     float64
		delta    float64
	}{
		{name: "equal logits are even odds", negative: 1.5, positive: 1.5, want: 0.5, delta: 1e-9},
		{name: "strong positive saturates high", negative: -4, positive: 4, want: 0.99966, delta: 1e-4},
		{name: "strong negative saturates low", negative: 4, positive: -4, want: 0.00034, delta: 1e-4},
		{name: "large logits stay finite", negative: -400, positive: 400, want: 1, delta: 1e-9},
		{name: "large inverted logits stay finite", negative: 400, positive: -400, want: 0, delta: 1e-9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := positiveProbability(tt.negative, tt.positive)
			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Fatalf("positiveProbability = %v, want a finite probability", got)
			}
			if math.Abs(got-tt.want) > tt.delta {
				t.Errorf("positiveProbability = %v, want %v within %v", got, tt.want, tt.delta)
			}
		})
	}
}
