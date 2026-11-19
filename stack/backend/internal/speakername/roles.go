package speakername

import (
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

const (
	// guestShare is the fraction of the longest speaker's time below which a
	// voice is marginal, and guestTurnRatio the fraction of the busiest
	// voice's turn count up to which it is also occasional. A guest is both:
	// an interviewer asking short questions speaks little but takes as many
	// turns as anyone.
	guestShare     = 0.15
	guestTurnRatio = 0.5
	// leadShare is the fraction of the longest speaker's time from which a
	// voice counts among the leading speakers.
	leadShare = 0.6
	// moderatorTurnRatio is how short a voice's average turn must be, against
	// the leading speakers' average, to read as moderation rather than debate.
	moderatorTurnRatio = 0.5
)

type speakingStats struct {
	total time.Duration
	turns int
}

func (s speakingStats) meanTurn() time.Duration {
	if s.turns == 0 {
		return 0
	}
	return s.total / time.Duration(s.turns)
}

// inferRoles classifies every label from the structure of the conversation:
// principals hold long turns and most of the time, moderators speak less and in
// short turns, a guest is marginal and occasional. A transcript with no usable timing yields
// unknown roles rather than a guess.
func inferRoles(turns []Turn) map[string]domain.SpeakerRole {
	stats := make(map[string]speakingStats)
	var longest time.Duration
	var mostTurns int
	for _, t := range turns {
		s := stats[t.Label]
		s.total += max(0, t.End-t.Start)
		s.turns++
		stats[t.Label] = s
		longest = max(longest, s.total)
		mostTurns = max(mostTurns, s.turns)
	}

	roles := make(map[string]domain.SpeakerRole, len(stats))
	if longest == 0 {
		for label := range stats {
			roles[label] = domain.SpeakerRoleUnknown
		}
		return roles
	}

	var leadTotal time.Duration
	var leadTurns int
	for _, s := range stats {
		if float64(s.total) >= leadShare*float64(longest) {
			leadTotal += s.total
			leadTurns += s.turns
		}
	}
	leadMean := leadTotal / time.Duration(leadTurns)

	for label, s := range stats {
		share := float64(s.total) / float64(longest)
		switch {
		case share < guestShare && float64(s.turns) <= guestTurnRatio*float64(mostTurns):
			roles[label] = domain.SpeakerRoleGuest
		case share < leadShare && float64(s.meanTurn()) < moderatorTurnRatio*float64(leadMean):
			roles[label] = domain.SpeakerRoleModerator
		default:
			roles[label] = domain.SpeakerRolePrincipal
		}
	}
	return roles
}
