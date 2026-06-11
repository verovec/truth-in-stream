package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// Store satisfies the video records port.
var _ domain.VideoStore = (*Store)(nil)

// CreateVideo inserts a new video record. The id and timestamps are assigned by
// the database and returned on the stored record.
func (s *Store) CreateVideo(ctx context.Context, v domain.Video) (domain.Video, error) {
	if !v.Kind.Valid() {
		return domain.Video{}, fmt.Errorf("postgres: create video: invalid kind %q", v.Kind)
	}
	if !v.Status.Valid() {
		return domain.Video{}, fmt.Errorf("postgres: create video: invalid status %q", v.Status)
	}
	row, err := s.queries.CreateVideo(ctx, db.CreateVideoParams{
		Title:       v.Title,
		ObjectKey:   v.ObjectKey,
		ContentType: v.ContentType,
		SizeBytes:   v.SizeBytes,
		Status:      string(v.Status),
		Kind:        string(v.Kind),
	})
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: create video: %w", err)
	}
	return videoFromRow(row), nil
}

// GetVideo returns the record with the given id. An unparseable id, like a
// missing row, maps to domain.ErrVideoNotFound: neither can name a real record.
func (s *Store) GetVideo(ctx context.Context, id string) (domain.Video, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	row, err := s.queries.GetVideo(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: get video %s: %w", id, err)
	}
	return videoFromRow(row), nil
}

// ListVideos returns every record, newest first.
func (s *Store) ListVideos(ctx context.Context) ([]domain.Video, error) {
	rows, err := s.queries.ListVideos(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: list videos: %w", err)
	}
	videos := make([]domain.Video, 0, len(rows))
	for _, r := range rows {
		videos = append(videos, videoFromRow(r))
	}
	return videos, nil
}

// SetVideoStatus updates the status of the record with the given id and returns
// the updated record, or domain.ErrVideoNotFound.
func (s *Store) SetVideoStatus(ctx context.Context, id string, status domain.VideoStatus) (domain.Video, error) {
	if !status.Valid() {
		return domain.Video{}, fmt.Errorf("postgres: set video status %s: invalid status %q", id, status)
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	row, err := s.queries.SetVideoStatus(ctx, db.SetVideoStatusParams{ID: uid, Status: string(status)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: set video status %s: %w", id, err)
	}
	return videoFromRow(row), nil
}

// UpsertSampleVideo inserts or updates a curated sample keyed by its object key,
// so seeding the same sample repeatedly is idempotent and keeps a stable id.
func (s *Store) UpsertSampleVideo(ctx context.Context, v domain.Video) (domain.Video, error) {
	if !v.Kind.Valid() {
		return domain.Video{}, fmt.Errorf("postgres: upsert sample video: invalid kind %q", v.Kind)
	}
	if !v.Status.Valid() {
		return domain.Video{}, fmt.Errorf("postgres: upsert sample video: invalid status %q", v.Status)
	}
	row, err := s.queries.UpsertSampleVideo(ctx, db.UpsertSampleVideoParams{
		Title:       v.Title,
		ObjectKey:   v.ObjectKey,
		ContentType: v.ContentType,
		SizeBytes:   v.SizeBytes,
		Status:      string(v.Status),
		Kind:        string(v.Kind),
	})
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: upsert sample video %q: %w", v.ObjectKey, err)
	}
	return videoFromRow(row), nil
}

// videoFromRow maps a generated row to the domain type. The stored UUID renders
// as its canonical string form and the timestamps drop their pgtype wrapper.
func videoFromRow(r db.Video) domain.Video {
	return domain.Video{
		ID:          r.ID.String(),
		Title:       r.Title,
		ObjectKey:   r.ObjectKey,
		ContentType: r.ContentType,
		SizeBytes:   r.SizeBytes,
		Status:      domain.VideoStatus(r.Status),
		Kind:        domain.VideoKind(r.Kind),
		CreatedAt:   r.CreatedAt.Time,
		UpdatedAt:   r.UpdatedAt.Time,
	}
}
