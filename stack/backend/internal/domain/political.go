package domain

import (
	"context"
	"time"
)

// LiteralVerdict is the objective accuracy axis of a political fact-check: does
// the literal proposition hold, independent of any context/manipulation flags.
type LiteralVerdict string

const (
	// LiteralAccurate means the literal proposition is correct.
	LiteralAccurate LiteralVerdict = "accurate"
	// LiteralInaccurate means the literal proposition is false.
	LiteralInaccurate LiteralVerdict = "inaccurate"
	// LiteralUnverifiable means the proposition cannot be objectively checked
	// (e.g. a subjective claim) on the available evidence.
	LiteralUnverifiable LiteralVerdict = "unverifiable"
)

// Valid reports whether v is one of the known literal verdicts. It mirrors the
// CHECK constraint on political_claims.literal_verdict so bad data is rejected
// before it reaches the store.
func (v LiteralVerdict) Valid() bool {
	switch v {
	case LiteralAccurate, LiteralInaccurate, LiteralUnverifiable:
		return true
	default:
		return false
	}
}

// ManipulationFlag is one orthogonal context/manipulation tag on a claim. A
// claim can carry zero or more, independent of its literal verdict, so
// "accurate, but cherry-picked timeframe" is expressible.
type ManipulationFlag string

const (
	// FlagMissingContext marks a claim that omits context that changes its meaning.
	FlagMissingContext ManipulationFlag = "missing-context"
	// FlagCherryPicked marks a true figure selected from a misleading timeframe or subset.
	FlagCherryPicked ManipulationFlag = "cherry-picked"
	// FlagOutdated marks a claim that was once true but no longer is.
	FlagOutdated ManipulationFlag = "outdated"
	// FlagMisattributed marks a quote or position attributed to the wrong source.
	FlagMisattributed ManipulationFlag = "misattributed"
	// FlagMisleadingCausation marks a correlation framed as causation.
	FlagMisleadingCausation ManipulationFlag = "misleading-causation"
)

// Valid reports whether f is one of the known manipulation flags.
func (f ManipulationFlag) Valid() bool {
	switch f {
	case FlagMissingContext, FlagCherryPicked, FlagOutdated, FlagMisattributed, FlagMisleadingCausation:
		return true
	default:
		return false
	}
}

// PoliticalClaim is a curated, pre-checked political claim and its embedding, as
// stored in the political claim DB and matched semantically against spoken
// segments. Embedding is voyage-4-large, EmbeddingDim dimensions. CheckedAt is
// when the outlet published its check; the zero value stores SQL NULL.
type PoliticalClaim struct {
	ID             string
	Text           string
	LiteralVerdict LiteralVerdict
	Flags          []ManipulationFlag
	SourceName     string
	SourceURL      string
	QuotedSpan     string
	Outlet         string
	CheckedAt      time.Time
	Embedding      []float32
}

// PoliticalClaimMatch is a retrieval hit from the political claim DB. Distance is
// cosine distance in [0, 2]; lower is more similar. The embedding itself is not
// returned.
type PoliticalClaimMatch struct {
	ID             string
	Text           string
	LiteralVerdict LiteralVerdict
	Flags          []ManipulationFlag
	SourceName     string
	SourceURL      string
	QuotedSpan     string
	Outlet         string
	CheckedAt      time.Time
	Distance       float32
}

// PoliticalClaimStore is the port for curated political claim storage and
// approximate nearest-neighbor retrieval (the fast-path matcher borrows an
// instant verdict for a repeated talking point).
type PoliticalClaimStore interface {
	// UpsertPoliticalClaim inserts or replaces one curated claim with its embedding.
	UpsertPoliticalClaim(ctx context.Context, claim PoliticalClaim) error
	// SearchPoliticalClaims returns the topK claims closest to query by cosine
	// distance, nearest first.
	SearchPoliticalClaims(ctx context.Context, query []float32, topK int) ([]PoliticalClaimMatch, error)
}

// VotePosition is a recorded position on a scrutin (a vote in the Assemblee
// Nationale or Senat).
type VotePosition string

const (
	// VoteFor is a recorded vote in favor.
	VoteFor VotePosition = "for"
	// VoteAgainst is a recorded vote against.
	VoteAgainst VotePosition = "against"
	// VoteAbstain is a recorded abstention.
	VoteAbstain VotePosition = "abstain"
	// VoteAbsent means the person did not take part in the scrutin.
	VoteAbsent VotePosition = "absent"
)

// Valid reports whether p is one of the known vote positions. It mirrors the
// CHECK constraint on voting_records.position.
func (p VotePosition) Valid() bool {
	switch p {
	case VoteFor, VoteAgainst, VoteAbstain, VoteAbsent:
		return true
	default:
		return false
	}
}

// Chamber identifies which assembly a scrutin belongs to.
type Chamber string

const (
	// ChamberAssemblee is the Assemblee Nationale.
	ChamberAssemblee Chamber = "assemblee"
	// ChamberSenat is the Senat.
	ChamberSenat Chamber = "senat"
)

// Valid reports whether c is one of the known chambers. It mirrors the CHECK
// constraint on voting_records.chamber.
func (c Chamber) Valid() bool {
	switch c {
	case ChamberAssemblee, ChamberSenat:
		return true
	default:
		return false
	}
}

// VotingRecord is one person's recorded position on one dated scrutin, queried
// relationally by (person, bill, date) - never by cosine.
type VotingRecord struct {
	PersonID   string
	PersonName string
	Chamber    Chamber
	ScrutinID  string
	BillTitle  string
	VotedOn    time.Time
	Position   VotePosition
	SourceURL  string
}

// VotingStore is the port for the structured voting-record store. The voting
// source adapter answers "did X vote for/against bill Y" by an exact relational
// lookup, so there is no embedding here.
type VotingStore interface {
	// UpsertVotingRecord inserts or replaces one recorded position (keyed by
	// person + scrutin).
	UpsertVotingRecord(ctx context.Context, record VotingRecord) error
	// LookupVotingRecords returns every recorded position for one person on one
	// bill on one date.
	LookupVotingRecords(ctx context.Context, personID, billTitle string, votedOn time.Time) ([]VotingRecord, error)
}
