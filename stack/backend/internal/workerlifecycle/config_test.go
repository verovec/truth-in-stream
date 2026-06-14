package workerlifecycle

import (
	"testing"
	"time"
)

func TestParseScalingConfig(t *testing.T) {
	t.Parallel()
	t.Run("empty payload yields empty map", func(t *testing.T) {
		t.Parallel()
		got, err := ParseScalingConfig(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %v", got)
		}
	})

	t.Run("empty object yields empty map", func(t *testing.T) {
		t.Parallel()
		got, err := ParseScalingConfig([]byte("{}"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %v", got)
		}
	})

	t.Run("valid config decodes", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"embedworker":{"queue_base":"embedding.jobs","ratio":200,"min":1,"max":8,"cooldown_seconds":180}}`)
		got, err := ParseScalingConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := ServiceScaling{QueueBase: "embedding.jobs", Ratio: 200, Min: 1, Max: 8, Cooldown: 180 * time.Second}
		if got["embedworker"] != want {
			t.Fatalf("got %+v, want %+v", got["embedworker"], want)
		}
	})

	t.Run("max zero is allowed (disabled service)", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"embedworker":{"queue_base":"embedding.jobs","ratio":200,"min":1,"max":0,"cooldown_seconds":60}}`)
		got, err := ParseScalingConfig(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["embedworker"].Max != 0 {
			t.Fatalf("expected max 0, got %d", got["embedworker"].Max)
		}
	})

	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed json", raw: `{`},
		{name: "missing queue_base", raw: `{"w":{"ratio":1,"min":0,"max":1}}`},
		{name: "non-positive ratio", raw: `{"w":{"queue_base":"q","ratio":0,"min":0,"max":1}}`},
		{name: "negative min", raw: `{"w":{"queue_base":"q","ratio":1,"min":-1,"max":1}}`},
		{name: "negative cooldown", raw: `{"w":{"queue_base":"q","ratio":1,"min":0,"max":1,"cooldown_seconds":-5}}`},
		{name: "max below min", raw: `{"w":{"queue_base":"q","ratio":1,"min":5,"max":2}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseScalingConfig([]byte(tc.raw)); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}
