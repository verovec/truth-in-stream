package service

// VerdictCredible, VerdictDisputed, and VerdictUnverifiable are the credibility
// verdict states the verify path emits per claim. They mirror the verify package's
// labels so a verdict the verifier returned, a borrowed curated verdict, and the
// aggregator's input all live in one vocabulary, without the service layer
// importing the adapter package. Credible, disputed, and unverifiable each move
// their own per-speaker count; none is weighted against the others.
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
// no direct sources; it does not change which per-speaker count a verdict moves.
const (
	BasisEvidence  = "evidence"
	BasisKnowledge = "knowledge"
)

// SpeakerTally is the running per-speaker breakdown for one speaker, emitted on a
// LiveEventSpeakerTally after each of that speaker's claim verdicts updates the
// counts. Credible, Disputed, and Unverifiable are the lifetime verdict tallies,
// so the UI can show how many checkable claims the speaker made and how they broke
// down - with no rolled-up trust number behind them.
//
// MisleadingFraming is the political path's separate count of this speaker's
// claims that carried at least one manipulation flag (cherry-picked, missing
// context, ...). It is orthogonal to the credibility tallies: a claim can be
// literally accurate (counted Credible) yet carry a flag (counted
// MisleadingFraming), so the UI can distinguish an outright falsehood from
// honest-but-misleading framing. It stays zero on the credibility-only
// (non-political) verify path, which emits no flags.
type SpeakerTally struct {
	Speaker           string
	Credible          int
	Disputed          int
	Unverifiable      int
	MisleadingFraming int
}

// speakerCredibility is the pure, per-speaker verdict tally. It accumulates lifetime
// counts of credible, disputed, and unverifiable verdicts plus the orthogonal
// misleading-framing count. It is not safe for concurrent use on its own;
// speakerMemory serializes access. The zero value is a valid empty tally.
type speakerCredibility struct {
	// Lifetime verdict counts, surfaced as the per-speaker breakdown.
	credible     int
	disputed     int
	unverifiable int
	// misleadingFraming counts this speaker's claims that carried at least one
	// manipulation flag, orthogonal to the credibility tallies above. It moves only
	// on the political path; the credibility-only path emits no flags and never
	// touches it.
	misleadingFraming int
}

// observe folds one claim verdict into the tally and returns the updated snapshot.
// Each of credible, disputed, and unverifiable moves only its own count - an
// unverifiable verdict ("we could not check this") is tracked exactly like the
// other two. An unknown state is ignored (no tally move), so a future verdict label
// cannot silently distort the breakdown.
func (s *speakerCredibility) observe(state string) SpeakerTally {
	switch state {
	case VerdictCredible:
		s.credible++
	case VerdictDisputed:
		s.disputed++
	case VerdictUnverifiable:
		s.unverifiable++
	}
	return s.snapshot()
}

// observeFraming records that the speaker's just-counted claim carried at least
// one manipulation flag, bumping the misleading-framing tally and returning the
// updated snapshot. It is orthogonal to observe: the credible/disputed/unverifiable
// counts are untouched, since a flag judges the framing, not the literal truth. It
// is the political path's second axis and is never called on the credibility-only
// path.
func (s *speakerCredibility) observeFraming() SpeakerTally {
	s.misleadingFraming++
	return s.snapshot()
}

// snapshot returns the current tallies without mutating the aggregator.
func (s *speakerCredibility) snapshot() SpeakerTally {
	return SpeakerTally{
		Credible:          s.credible,
		Disputed:          s.disputed,
		Unverifiable:      s.unverifiable,
		MisleadingFraming: s.misleadingFraming,
	}
}
