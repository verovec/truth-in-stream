package domain

import (
	"errors"
	"time"
)

// ErrVideoSpeakerNotFound is returned when a speaker update names a diarized
// label the video's stored analysis never produced.
var ErrVideoSpeakerNotFound = errors.New("video speaker not found")

// ErrInvalidSpeakerRole is returned when a speaker update carries a role
// outside the known set.
var ErrInvalidSpeakerRole = errors.New("invalid speaker role")

// ErrInvalidSpeakerName is returned when a speaker update carries a name
// longer than the accepted bound.
var ErrInvalidSpeakerName = errors.New("invalid speaker name")

// SpeakerRole classifies a diarized voice by its part in the program.
type SpeakerRole string

// The roles a speaker can hold. Unknown is the role of a voice the structure
// signals could not classify.
const (
	SpeakerRolePrincipal SpeakerRole = "principal"
	SpeakerRoleModerator SpeakerRole = "moderator"
	SpeakerRoleGuest     SpeakerRole = "guest"
	SpeakerRoleUnknown   SpeakerRole = "unknown"
)

// Valid reports whether r is one of the known roles.
func (r SpeakerRole) Valid() bool {
	switch r {
	case SpeakerRolePrincipal, SpeakerRoleModerator, SpeakerRoleGuest, SpeakerRoleUnknown:
		return true
	default:
		return false
	}
}

// SpeakerMethod records how a speaker's name was decided.
type SpeakerMethod string

// The naming methods. Signals is the deterministic fusion of transcript
// signals, TieBreak a model decision validated against the transcript,
// Operator a human override, and None a label left unnamed.
const (
	SpeakerMethodSignals  SpeakerMethod = "signals"
	SpeakerMethodTieBreak SpeakerMethod = "tie_break"
	SpeakerMethodOperator SpeakerMethod = "operator"
	SpeakerMethodNone     SpeakerMethod = "none"
)

// SpeakerEvidenceKind names the transcript signal behind one piece of evidence.
type SpeakerEvidenceKind string

// The evidence kinds, one per naming signal.
const (
	SpeakerEvidenceHandoff       SpeakerEvidenceKind = "handoff"
	SpeakerEvidenceVocative      SpeakerEvidenceKind = "vocative"
	SpeakerEvidenceSelfReference SpeakerEvidenceKind = "self_reference"
	SpeakerEvidenceTieBreak      SpeakerEvidenceKind = "tie_break"
)

// SpeakerEvidence is one quoted transcript span supporting a speaker's name.
// Quote is verbatim transcript text and At the video time of the turn it was
// taken from.
type SpeakerEvidence struct {
	Kind  SpeakerEvidenceKind
	Quote string
	At    time.Duration
}

// VideoSpeaker maps one diarized label of a video to a person. Name is empty
// for a label left unnamed. OperatorSet marks a mapping a human confirmed or
// overrode; the naming step never rewrites such a row.
type VideoSpeaker struct {
	VideoID     string
	Label       string
	Name        string
	Role        SpeakerRole
	Confidence  float64
	Method      SpeakerMethod
	Evidence    []SpeakerEvidence
	OperatorSet bool
	UpdatedAt   time.Time
}
