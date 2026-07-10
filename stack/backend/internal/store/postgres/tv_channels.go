package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// Store satisfies the TV channel records port.
var _ domain.TVChannelStore = (*Store)(nil)

// uniqueViolation is the Postgres SQLSTATE for a unique-constraint violation.
const uniqueViolation = "23505"

// CreateTVChannel inserts a new channel. A slug collision maps to
// domain.ErrDuplicateTVChannelSlug so the handler answers 409 rather than 500.
func (s *Store) CreateTVChannel(ctx context.Context, c domain.TVChannel) (domain.TVChannel, error) {
	if !c.SourceKind.Valid() {
		return domain.TVChannel{}, fmt.Errorf("postgres: create tv channel: invalid source kind %q", c.SourceKind)
	}
	row, err := s.queries.CreateTVChannel(ctx, db.CreateTVChannelParams{
		Slug:           c.Slug,
		Name:           c.Name,
		SourceKind:     string(c.SourceKind),
		SourceRef:      c.SourceRef,
		Enabled:        c.Enabled,
		ArchiveEnabled: c.ArchiveEnabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return domain.TVChannel{}, domain.ErrDuplicateTVChannelSlug
		}
		return domain.TVChannel{}, fmt.Errorf("postgres: create tv channel: %w", err)
	}
	return tvChannelFromRow(row), nil
}

// GetTVChannel returns the record with the given id. An unparseable id, like a
// missing row, maps to domain.ErrTVChannelNotFound: neither names a real record.
func (s *Store) GetTVChannel(ctx context.Context, id string) (domain.TVChannel, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.TVChannel{}, domain.ErrTVChannelNotFound
	}
	row, err := s.queries.GetTVChannel(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TVChannel{}, domain.ErrTVChannelNotFound
	}
	if err != nil {
		return domain.TVChannel{}, fmt.Errorf("postgres: get tv channel %s: %w", id, err)
	}
	return tvChannelFromRow(row), nil
}

// ListTVChannels returns every channel, ordered by name.
func (s *Store) ListTVChannels(ctx context.Context) ([]domain.TVChannel, error) {
	rows, err := s.queries.ListTVChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: list tv channels: %w", err)
	}
	channels := make([]domain.TVChannel, 0, len(rows))
	for _, r := range rows {
		channels = append(channels, tvChannelFromRow(r))
	}
	return channels, nil
}

// UpdateTVChannel writes the channel's mutable fields by id and returns the
// updated record, or domain.ErrTVChannelNotFound.
func (s *Store) UpdateTVChannel(ctx context.Context, c domain.TVChannel) (domain.TVChannel, error) {
	if !c.SourceKind.Valid() {
		return domain.TVChannel{}, fmt.Errorf("postgres: update tv channel: invalid source kind %q", c.SourceKind)
	}
	uid, err := uuid.Parse(c.ID)
	if err != nil {
		return domain.TVChannel{}, domain.ErrTVChannelNotFound
	}
	row, err := s.queries.UpdateTVChannel(ctx, db.UpdateTVChannelParams{
		ID:             uid,
		Name:           c.Name,
		SourceKind:     string(c.SourceKind),
		SourceRef:      c.SourceRef,
		Enabled:        c.Enabled,
		ArchiveEnabled: c.ArchiveEnabled,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TVChannel{}, domain.ErrTVChannelNotFound
	}
	if err != nil {
		return domain.TVChannel{}, fmt.Errorf("postgres: update tv channel %s: %w", c.ID, err)
	}
	return tvChannelFromRow(row), nil
}

// DeleteTVChannel removes the record with the given id. An unparseable id, like
// a missing row, maps to domain.ErrTVChannelNotFound. Recordings survive: the
// videos.channel_id FK is ON DELETE SET NULL.
func (s *Store) DeleteTVChannel(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrTVChannelNotFound
	}
	deleted, err := s.queries.DeleteTVChannel(ctx, uid)
	if err != nil {
		return fmt.Errorf("postgres: delete tv channel %s: %w", id, err)
	}
	if deleted == 0 {
		return domain.ErrTVChannelNotFound
	}
	return nil
}

// UpsertTVChannelBySlug inserts or refreshes a seed channel keyed by its slug,
// so reseeding is idempotent and keeps a stable id. Operator-controlled state
// (enabled, archive_enabled) is preserved on conflict.
func (s *Store) UpsertTVChannelBySlug(ctx context.Context, c domain.TVChannel) (domain.TVChannel, error) {
	if !c.SourceKind.Valid() {
		return domain.TVChannel{}, fmt.Errorf("postgres: upsert tv channel: invalid source kind %q", c.SourceKind)
	}
	row, err := s.queries.UpsertTVChannelBySlug(ctx, db.UpsertTVChannelBySlugParams{
		Slug:           c.Slug,
		Name:           c.Name,
		SourceKind:     string(c.SourceKind),
		SourceRef:      c.SourceRef,
		Enabled:        c.Enabled,
		ArchiveEnabled: c.ArchiveEnabled,
	})
	if err != nil {
		return domain.TVChannel{}, fmt.Errorf("postgres: upsert tv channel %q: %w", c.Slug, err)
	}
	return tvChannelFromRow(row), nil
}

// tvChannelFromRow maps a generated row to the domain type: the stored UUID
// renders as its canonical string form and the timestamps drop their pgtype
// wrapper.
func tvChannelFromRow(r db.TvChannel) domain.TVChannel {
	return domain.TVChannel{
		ID:             r.ID.String(),
		Slug:           r.Slug,
		Name:           r.Name,
		SourceKind:     domain.TVSourceKind(r.SourceKind),
		SourceRef:      r.SourceRef,
		Enabled:        r.Enabled,
		ArchiveEnabled: r.ArchiveEnabled,
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
	}
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation, so a slug collision surfaces as a domain sentinel, not a 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
