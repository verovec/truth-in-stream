package config

import "testing"

func TestLoadSpeakerNaming(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		want       SpeakerNaming
		wantActive bool
		wantErr    bool
	}{
		{
			name: "on by default, model inactive without a key",
			want: SpeakerNaming{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: true},
		},
		{
			name:       "deepseek key activates the model",
			env:        map[string]string{"DEEPSEEK_API_KEY": "d-test"},
			want:       SpeakerNaming{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d-test"}, Enabled: true},
			wantActive: true,
		},
		{
			name: "anthropic reads the per-stage key and default model",
			env:  map[string]string{"LLM_PROVIDER": "anthropic", "SPEAKER_NAMING_API_KEY": "sk-test"},
			want: SpeakerNaming{
				LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "sk-test", Model: defaultStageModel,
			},
			wantActive: true,
		},
		{
			name: "the per-stage key does not activate another provider",
			env:  map[string]string{"SPEAKER_NAMING_API_KEY": "sk-test"},
			want: SpeakerNaming{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: true, APIKey: "sk-test"},
		},
		{
			name:       "disabled with a model override",
			env:        map[string]string{"SPEAKER_NAMING_ENABLED": "false", "SPEAKER_NAMING_MODEL": "custom", "DEEPSEEK_API_KEY": "d-test"},
			want:       SpeakerNaming{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d-test"}, Model: "custom"},
			wantActive: true,
		},
		{name: "non-bool enabled rejected", env: map[string]string{"SPEAKER_NAMING_ENABLED": "maybe"}, wantErr: true},
		{name: "unknown provider rejected", env: map[string]string{"LLM_PROVIDER": "mistral"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"LLM_PROVIDER", "DEEPSEEK_API_KEY", "GEMINI_API_KEY", "SPEAKER_NAMING_ENABLED", "SPEAKER_NAMING_API_KEY", "SPEAKER_NAMING_MODEL"} {
				t.Setenv(key, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadSpeakerNaming()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if got.ModelActive() != tc.wantActive {
				t.Errorf("ModelActive = %v, want %v", got.ModelActive(), tc.wantActive)
			}
		})
	}
}
