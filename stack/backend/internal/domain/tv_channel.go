package domain

import (
	"context"
	"errors"
	"time"
)

// ErrTVChannelNotFound is returned by a TVChannelStore when no record matches
// the given id. Callers detect it with errors.Is and map it to a 404; it never
// wraps a transport type.
var ErrTVChannelNotFound = errors.New("tv channel not found")

// ErrDuplicateTVChannelSlug is returned when creating a channel whose slug
// already exists. slug is the stable registry key, unique across the catalog,
// so the create path maps the store's unique-violation to this sentinel and the
// handler answers 409.
var ErrDuplicateTVChannelSlug = errors.New("tv channel slug already exists")

// TVSourceKind is how the capture worker resolves a channel's live stream: an
// official YouTube live simulcast (resolved through streamlink) or a direct HLS
// manifest (a parliamentary video portal). DRM'd broadcaster players are out of
// scope, so those two kinds are the whole set.
type TVSourceKind string

const (
	// TVSourceYouTube is an official 24/7 YouTube live simulcast.
	TVSourceYouTube TVSourceKind = "youtube"
	// TVSourceHLS is a direct HLS manifest, the parliamentary portals.
	TVSourceHLS TVSourceKind = "hls"
)

// Valid reports whether k is a known source kind. It mirrors the DB CHECK on
// tv_channels.source_kind so a bad kind is rejected before it reaches storage.
func (k TVSourceKind) Valid() bool {
	switch k {
	case TVSourceYouTube, TVSourceHLS:
		return true
	default:
		return false
	}
}

// TVChannel is a television channel the platform can capture and fact-check. ID
// is the canonical string form of the row's UUID; Slug is the stable key used
// in storage paths and seeds. Enabled is the single capture switch and
// ArchiveEnabled is the per-channel recording opt-out; live status is not a
// stored field (the hub derives it from a connected publisher feed).
type TVChannel struct {
	ID             string
	Slug           string
	Name           string
	SourceKind     TVSourceKind
	SourceRef      string
	Enabled        bool
	ArchiveEnabled bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TVChannelStore is the persistence port for TV channel records, implemented by
// internal/store/postgres. It holds no transport types.
type TVChannelStore interface {
	// CreateTVChannel inserts c (its ID, CreatedAt, and UpdatedAt are assigned
	// by the store) and returns the stored record, or ErrDuplicateTVChannelSlug
	// when its slug already exists.
	CreateTVChannel(ctx context.Context, c TVChannel) (TVChannel, error)
	// GetTVChannel returns the record with the given id, or ErrTVChannelNotFound.
	GetTVChannel(ctx context.Context, id string) (TVChannel, error)
	// ListTVChannels returns every channel, ordered by name.
	ListTVChannels(ctx context.Context) ([]TVChannel, error)
	// UpdateTVChannel writes c's mutable fields (name, source, toggles) by id and
	// returns the updated record, or ErrTVChannelNotFound.
	UpdateTVChannel(ctx context.Context, c TVChannel) (TVChannel, error)
	// DeleteTVChannel removes the record with the given id, or returns
	// ErrTVChannelNotFound when no record matches. Recordings survive: the
	// videos.channel_id FK nulls out (ON DELETE SET NULL).
	DeleteTVChannel(ctx context.Context, id string) error
	// UpsertTVChannelBySlug inserts or refreshes a seed channel keyed by its
	// slug, so reseeding is idempotent and keeps a stable id. Operator-controlled
	// state (enabled, archive_enabled) is preserved on conflict, never re-armed.
	UpsertTVChannelBySlug(ctx context.Context, c TVChannel) (TVChannel, error)
}
