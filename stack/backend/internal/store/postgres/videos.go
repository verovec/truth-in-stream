package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
	channelID, err := optionalUUID(v.ChannelID)
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: create video: invalid channel id %q: %w", v.ChannelID, err)
	}
	row, err := s.queries.CreateVideo(ctx, db.CreateVideoParams{
		Title:       v.Title,
		ObjectKey:   v.ObjectKey,
		ContentType: v.ContentType,
		SizeBytes:   v.SizeBytes,
		Status:      string(v.Status),
		Kind:        string(v.Kind),
		ChannelID:   channelID,
		RecordedAt:  timestamptzValue(v.RecordedAt),
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

// GetVideoByObjectKey returns the record with the given storage object key, or
// domain.ErrVideoNotFound. It mirrors GetVideo's no-rows mapping so an absent
// recording is a recognizable not-found, letting an idempotent writer resolve
// its own prior insert by the deterministic key instead of colliding on it.
func (s *Store) GetVideoByObjectKey(ctx context.Context, key string) (domain.Video, error) {
	row, err := s.queries.GetVideoByObjectKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: get video by object key %q: %w", key, err)
	}
	return videoFromRow(row), nil
}

// ListTVRecordingsBefore returns every kind `tv` recording captured before
// cutoff, oldest first, for retention pruning.
func (s *Store) ListTVRecordingsBefore(ctx context.Context, cutoff time.Time) ([]domain.Video, error) {
	rows, err := s.queries.ListTVRecordingsBefore(ctx, timestamptzValue(cutoff))
	if err != nil {
		return nil, fmt.Errorf("postgres: list tv recordings before %s: %w", cutoff, err)
	}
	videos := make([]domain.Video, 0, len(rows))
	for _, r := range rows {
		videos = append(videos, videoFromRow(r))
	}
	return videos, nil
}

// ListTVRecordingsByChannel returns every ready kind `tv` recording for the
// given channel, newest first, for the /tv page's recordings strip. An
// unparseable channel id names no channel, so it yields an empty list rather
// than an error, mirroring GetVideo's treatment of a malformed id.
func (s *Store) ListTVRecordingsByChannel(ctx context.Context, channelID string) ([]domain.Video, error) {
	uid, err := uuid.Parse(channelID)
	if err != nil {
		return []domain.Video{}, nil
	}
	rows, err := s.queries.ListTVRecordingsByChannel(ctx, uuid.NullUUID{UUID: uid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("postgres: list tv recordings for channel %s: %w", channelID, err)
	}
	videos := make([]domain.Video, 0, len(rows))
	for _, r := range rows {
		videos = append(videos, videoFromRow(r))
	}
	return videos, nil
}

// DeleteVideo removes the record with the given id. An unparseable id, like a
// missing row, maps to domain.ErrVideoNotFound: neither can name a real record.
func (s *Store) DeleteVideo(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.ErrVideoNotFound
	}
	deleted, err := s.queries.DeleteVideo(ctx, uid)
	if err != nil {
		return fmt.Errorf("postgres: delete video %s: %w", id, err)
	}
	if deleted == 0 {
		return domain.ErrVideoNotFound
	}
	return nil
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

// CreateYouTubeVideo inserts a pending YouTube ingest. The unique source id
// makes a repeat submission a no-op: the insert returns no row and this maps it
// to domain.ErrDuplicateSource so the caller can resolve the existing record.
func (s *Store) CreateYouTubeVideo(ctx context.Context, v domain.Video) (domain.Video, error) {
	if !v.Kind.Valid() {
		return domain.Video{}, fmt.Errorf("postgres: create youtube video: invalid kind %q", v.Kind)
	}
	if !v.Status.Valid() {
		return domain.Video{}, fmt.Errorf("postgres: create youtube video: invalid status %q", v.Status)
	}
	row, err := s.queries.CreateYouTubeVideo(ctx, db.CreateYouTubeVideoParams{
		Title:       v.Title,
		ObjectKey:   v.ObjectKey,
		ContentType: v.ContentType,
		SizeBytes:   v.SizeBytes,
		Status:      string(v.Status),
		Kind:        string(v.Kind),
		SourceUrl:   textValue(v.SourceURL),
		SourceID:    textValue(v.SourceID),
		DurationMs:  v.DurationMS,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrDuplicateSource
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: create youtube video: %w", err)
	}
	return videoFromRow(row), nil
}

// GetVideoBySourceID returns the record with the given canonical source id, or
// domain.ErrVideoNotFound.
func (s *Store) GetVideoBySourceID(ctx context.Context, sourceID string) (domain.Video, error) {
	row, err := s.queries.GetVideoBySourceID(ctx, textValue(sourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: get video by source %q: %w", sourceID, err)
	}
	return videoFromRow(row), nil
}

// SetVideoReady records a completed ingest's probed metadata and flips the
// record to ready, or returns domain.ErrVideoNotFound.
func (s *Store) SetVideoReady(ctx context.Context, id, title string, sizeBytes, durationMS int64) (domain.Video, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	row, err := s.queries.SetVideoReady(ctx, db.SetVideoReadyParams{
		ID:         uid,
		Title:      title,
		SizeBytes:  sizeBytes,
		DurationMs: durationMS,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: set video ready %s: %w", id, err)
	}
	return videoFromRow(row), nil
}

// RetryFailedVideo atomically flips a failed record back to pending so it can be
// re-ingested, returning the claimed record. A record that is not currently
// failed (already pending, ready, or gone) yields domain.ErrIngestNotRetriable,
// so two concurrent re-submissions cannot both re-download.
func (s *Store) RetryFailedVideo(ctx context.Context, id string) (domain.Video, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Video{}, domain.ErrIngestNotRetriable
	}
	row, err := s.queries.RetryFailedVideo(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrIngestNotRetriable
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: retry failed video %s: %w", id, err)
	}
	return videoFromRow(row), nil
}

// SetVideoFailed records the reason an ingest failed and flips the record to
// failed, or returns domain.ErrVideoNotFound.
func (s *Store) SetVideoFailed(ctx context.Context, id, reason string) (domain.Video, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	row, err := s.queries.SetVideoFailed(ctx, db.SetVideoFailedParams{ID: uid, Error: textValue(reason)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("postgres: set video failed %s: %w", id, err)
	}
	return videoFromRow(row), nil
}

// videoFromRow maps a generated row to the domain type. The stored UUID renders
// as its canonical string form, the timestamps drop their pgtype wrapper, and a
// NULL text, uuid, or timestamp column maps to the zero value (empty string /
// zero time).
func videoFromRow(r db.Video) domain.Video {
	return domain.Video{
		ID:          r.ID.String(),
		Title:       r.Title,
		ObjectKey:   r.ObjectKey,
		ContentType: r.ContentType,
		SizeBytes:   r.SizeBytes,
		Status:      domain.VideoStatus(r.Status),
		Kind:        domain.VideoKind(r.Kind),
		SourceURL:   r.SourceUrl.String,
		SourceID:    r.SourceID.String,
		DurationMS:  r.DurationMs,
		Error:       r.Error.String,
		ChannelID:   nullUUIDString(r.ChannelID),
		RecordedAt:  r.RecordedAt.Time,
		CreatedAt:   r.CreatedAt.Time,
		UpdatedAt:   r.UpdatedAt.Time,
	}
}

// textValue wraps a Go string as a pgtype.Text, mapping the empty string to SQL
// NULL so an absent source id or error is stored as NULL, not "".
func textValue(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// timestamptzValue wraps a Go time as a pgtype.Timestamptz, mapping the zero
// time to SQL NULL so an absent recorded_at is stored as NULL, not the epoch.
func timestamptzValue(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

// optionalUUID parses a canonical UUID string into a uuid.NullUUID, mapping the
// empty string to SQL NULL. A non-empty but malformed id is an error rather than
// a silent NULL, so a bad channel reference is caught at the write, not hidden.
func optionalUUID(s string) (uuid.NullUUID, error) {
	if s == "" {
		return uuid.NullUUID{}, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.NullUUID{}, err
	}
	return uuid.NullUUID{UUID: id, Valid: true}, nil
}

// nullUUIDString renders a uuid.NullUUID as its canonical string form, or the
// empty string when NULL.
func nullUUIDString(n uuid.NullUUID) string {
	if !n.Valid {
		return ""
	}
	return n.UUID.String()
}
