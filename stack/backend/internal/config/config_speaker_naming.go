package config

// SpeakerNaming configures the post-analysis step that names a video's
// diarized speakers. Enabled gates the automatic run after each completed
// pre-analysis (the on-demand re-run and the operator corrections stay
// available either way). The step's generative edge - listing participants and
// breaking ties - needs the selected provider's key: without it the step still
// runs, classifies roles, and leaves every speaker unnamed. APIKey is the
// Anthropic per-stage secret, read from the environment only and never logged.
type SpeakerNaming struct {
	LLMSelection
	Enabled bool
	APIKey  string
	Model   string
}

// ModelActive reports whether the step's model calls can be wired: the
// selected provider has the key it needs.
func (c SpeakerNaming) ModelActive() bool {
	return c.hasKey(c.APIKey)
}

// LoadSpeakerNaming reads the speaker naming configuration from the
// environment. SPEAKER_NAMING_ENABLED defaults to true; SPEAKER_NAMING_API_KEY
// is the Anthropic key used when LLM_PROVIDER is anthropic (the other providers
// read their global key); SPEAKER_NAMING_MODEL overrides the provider's default
// model.
func LoadSpeakerNaming() (SpeakerNaming, error) {
	llmSel, err := loadLLMSelection()
	if err != nil {
		return SpeakerNaming{}, err
	}
	c := SpeakerNaming{LLMSelection: llmSel}
	if c.Enabled, err = boolEnvDefault("SPEAKER_NAMING_ENABLED", true); err != nil {
		return SpeakerNaming{}, err
	}
	c.APIKey = getenv("SPEAKER_NAMING_API_KEY", "")
	c.Model = getenv("SPEAKER_NAMING_MODEL", llmSel.defaultModel())
	return c, nil
}
