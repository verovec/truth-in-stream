package domain_test

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestLiteralVerdictValid(t *testing.T) {
	t.Parallel()
	for _, v := range []domain.LiteralVerdict{domain.LiteralAccurate, domain.LiteralInaccurate, domain.LiteralUnverifiable} {
		if !v.Valid() {
			t.Errorf("LiteralVerdict %q should be valid", v)
		}
	}
	for _, v := range []domain.LiteralVerdict{"", "corroborates", "true", "ACCURATE"} {
		if v.Valid() {
			t.Errorf("LiteralVerdict %q should be invalid", v)
		}
	}
}

func TestManipulationFlagValid(t *testing.T) {
	t.Parallel()
	for _, f := range []domain.ManipulationFlag{
		domain.FlagMissingContext, domain.FlagCherryPicked, domain.FlagOutdated,
		domain.FlagMisattributed, domain.FlagMisleadingCausation,
	} {
		if !f.Valid() {
			t.Errorf("ManipulationFlag %q should be valid", f)
		}
	}
	for _, f := range []domain.ManipulationFlag{"", "biased", "cherrypicked"} {
		if f.Valid() {
			t.Errorf("ManipulationFlag %q should be invalid", f)
		}
	}
}

func TestVotePositionValid(t *testing.T) {
	t.Parallel()
	for _, p := range []domain.VotePosition{domain.VoteFor, domain.VoteAgainst, domain.VoteAbstain, domain.VoteAbsent} {
		if !p.Valid() {
			t.Errorf("VotePosition %q should be valid", p)
		}
	}
	for _, p := range []domain.VotePosition{"", "yes", "no", "FOR"} {
		if p.Valid() {
			t.Errorf("VotePosition %q should be invalid", p)
		}
	}
}

func TestChamberValid(t *testing.T) {
	t.Parallel()
	for _, c := range []domain.Chamber{domain.ChamberAssemblee, domain.ChamberSenat} {
		if !c.Valid() {
			t.Errorf("Chamber %q should be valid", c)
		}
	}
	for _, c := range []domain.Chamber{"", "congress", "ASSEMBLEE"} {
		if c.Valid() {
			t.Errorf("Chamber %q should be invalid", c)
		}
	}
}
