package domain

// Locale is the language the live LLM stages prompt and reason in, and the
// language code the transcriber biases toward. It is a single shared value so
// the transcription session, the check-worthiness gate, and the claim
// decomposer cannot drift onto different languages within one run.
//
// The zero value is the empty Locale, which every consumer treats as the
// default English behavior: the transcriber auto-detects (sends no
// language_code) and the LLM stages use their English prompts. A non-default
// Locale opts a stage into its localized prompt and, for transcription, the
// matching language_code.
type Locale string

const (
	// LocaleEnglish is the default locale: English prompts and provider-side
	// language auto-detection. It is the empty value so an unset Locale is the
	// pre-existing English behavior with no special-casing.
	LocaleEnglish Locale = ""
	// LocaleFrench selects French prompts for the live LLM stages and biases the
	// transcriber toward French. It is the locale of the French/EU political
	// fact-checking mode.
	LocaleFrench Locale = "fr"
)

// LanguageCode is the ISO-639 code the transcriber biases toward for this
// locale, or empty for provider-side auto-detection. English maps to empty
// (auto-detect, the prior behavior); French maps to "fr".
func (l Locale) LanguageCode() string {
	if l == LocaleFrench {
		return string(LocaleFrench)
	}
	return ""
}

// IsFrench reports whether the locale selects French. Stages branch on this to
// pick their French prompt; every other locale (including the default) keeps the
// English prompt.
func (l Locale) IsFrench() bool {
	return l == LocaleFrench
}
