package service

import "math"

// VerdictCredible, VerdictDisputed, and VerdictUnverifiable are the credibility
// verdict states the verify path emits per claim. They mirror the verify package's
// labels so a verdict the verifier returned, a borrowed curated verdict, and the
// aggregator's input all live in one vocabulary, without the service layer
// importing the adapter package. Only credible and disputed move a speaker's
// score; unverifiable is tallied but excluded from it.
const (
	VerdictCredible     = "credible"
	VerdictDisputed     = "disputed"
	VerdictUnverifiable = "unverifiable"
)

// LiteralAccurate, LiteralInaccurate, and LiteralUnverifiable are the political
// path's literal-axis verdict labels: is the claim, taken at face value, true
// against the routed evidence? They mirror the verify package's two-axis labels so
// a verdict the political verifier returned and the service's mapping onto the
// credibility axis (credibilityFromLiteral) live in one vocabulary without the
// service layer importing the adapter package.
const (
	LiteralAccurate     = "accurate"
	LiteralInaccurate   = "inaccurate"
	LiteralUnverifiable = "unverifiable"
)

// The manipulation-flag vocabulary for the political path's second axis, mirrored
// from the verify package so the service carries a flag through to the wire without
// importing the adapter. The set is closed; the verifier adapter's guard drops
// anything outside it before a flag reaches the live path.
const (
	FlagMissingContext      = "missing-context"
	FlagCherryPicked        = "cherry-picked"
	FlagOutdated            = "outdated"
	FlagMisattributed       = "misattributed"
	FlagMisleadingCausation = "misleading-causation"
)

// BasisEvidence and BasisKnowledge tag what a verdict rests on: a surviving
// citation, or the verifier's world-knowledge tiebreaker. The service carries the
// basis through to the wire so the UI can mark a knowledge-only verdict as having
// no direct sources; it does not affect the credibility score (the verifier
// already capped a knowledge verdict's confidence, which is what weights it).
const (
	BasisEvidence  = "evidence"
	BasisKnowledge = "knowledge"
)

// defaultPriorStrength is the Beta-Binomial prior pseudo-count k when a caller
// leaves it unset. A symmetric Beta(k/2, k/2) prior has mean 0.5 (neutral) and k
// pseudo-observations of weight: larger k shrinks harder toward neutral, so the
// score moves more slowly. The config layer mirrors this default
// (SPEAKER_SCORE_PRIOR_STRENGTH) and the two must stay in sync.
const defaultPriorStrength = 4.0

// SpeakerScore is the running credibility snapshot for one speaker, emitted on a
// LiveEventSpeakerScore after each of that speaker's claim verdicts updates the
// aggregate. Score is the Beta-Binomial posterior mean in [0,1]; Credible,
// Disputed, and Unverifiable are the lifetime verdict tallies, so the UI can show
// the score with its sample size and de-emphasize a thin one.
//
// MisleadingFraming is the political path's separate count of this speaker's
// claims that carried at least one manipulation flag (cherry-picked, missing
// context, ...). It is orthogonal to the credibility tallies and to the score: a
// claim can be literally accurate (counted Credible, moving the score up) yet
// carry a flag (counted MisleadingFraming), so the UI can distinguish an outright
// falsehood from honest-but-misleading framing. It stays zero on the
// credibility-only (non-political) verify path, which emits no flags.
type SpeakerScore struct {
	Speaker           string
	Score             float64
	Credible          int
	Disputed          int
	Unverifiable      int
	MisleadingFraming int
}

// speakerCredibility is the pure, per-speaker Beta-Binomial credibility tally. It
// shrinks a confidence-weighted true rate toward a neutral 0.5 prior so a speaker
// with one credible claim does not read as a confident 100%, and the score
// sharpens as they say more. It is not safe for concurrent use on its own;
// speakerMemory serializes access. Build it with newSpeakerCredibility - the zero
// value has a zero prior strength and would divide by zero on an empty history.
type speakerCredibility struct {
	priorStrength float64
	// successes and failures are the confidence-weighted credible/disputed mass
	// (S and F). Weighting by confidence means a tentative knowledge-only verdict
	// moves the score less than a strongly-evidenced one.
	successes float64
	failures  float64
	// Lifetime verdict counts, surfaced as the sample-size tally. unverifiable is
	// tracked but never enters the score.
	credible     int
	disputed     int
	unverifiable int
	// misleadingFraming counts this speaker's claims that carried at least one
	// manipulation flag, orthogonal to the credibility tallies above and to the
	// score. It moves only on the political path; the credibility-only path emits
	// no flags and never touches it.
	misleadingFraming int
}

// newSpeakerCredibility builds an empty aggregator with the given prior strength,
// falling back to the default when it is non-positive (a zero or negative k has no
// valid Beta prior).
func newSpeakerCredibility(priorStrength float64) *speakerCredibility {
	if priorStrength <= 0 {
		priorStrength = defaultPriorStrength
	}
	return &speakerCredibility{priorStrength: priorStrength}
}

// observe folds one claim verdict into the tally and returns the updated snapshot.
// A credible verdict adds its confidence to the success mass, a disputed verdict
// to the failure mass, and an unverifiable verdict moves only the tally - it is
// excluded from the score, since "we could not check this" is neither for nor
// against the speaker. An unknown state is ignored (no tally, no score move), so a
// future verdict label cannot silently skew the score. Confidence is clamped to
// [0,1] so a stray value cannot distort the weighting.
func (s *speakerCredibility) observe(state string, confidence float64) SpeakerScore {
	weight := clampConfidence01(confidence)
	switch state {
	case VerdictCredible:
		s.successes += weight
		s.credible++
	case VerdictDisputed:
		s.failures += weight
		s.disputed++
	case VerdictUnverifiable:
		s.unverifiable++
	}
	return s.snapshot()
}

// observeFraming records that the speaker's just-scored claim carried at least
// one manipulation flag, bumping the misleading-framing tally and returning the
// updated snapshot. It is orthogonal to observe: the score and the
// credible/disputed/unverifiable tallies are untouched, since a flag judges the
// framing, not the literal truth. It is the political path's second axis and is
// never called on the credibility-only path.
func (s *speakerCredibility) observeFraming() SpeakerScore {
	s.misleadingFraming++
	return s.snapshot()
}

// snapshot returns the current score and tallies without mutating the aggregator.
// The score is the Beta-Binomial posterior mean (k/2 + S) / (k + S + F): with no
// scored claims it is exactly 0.5, and it converges to the confidence-weighted
// true rate as claims accumulate.
func (s *speakerCredibility) snapshot() SpeakerScore {
	half := s.priorStrength / 2
	score := (half + s.successes) / (s.priorStrength + s.successes + s.failures)
	return SpeakerScore{
		Score:             score,
		Credible:          s.credible,
		Disputed:          s.disputed,
		Unverifiable:      s.unverifiable,
		MisleadingFraming: s.misleadingFraming,
	}
}

// clampConfidence01 bounds a confidence weight into [0,1], treating a NaN as 0 so a
// sentinel never distorts the score's weighting.
func clampConfidence01(c float64) float64 {
	switch {
	case math.IsNaN(c) || c < 0:
		return 0
	case c > 1:
		return 1
	default:
		return c
	}
}
