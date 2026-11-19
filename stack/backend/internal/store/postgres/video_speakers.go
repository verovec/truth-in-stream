package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// speakerEvidenceJSON is the jsonb form of one piece of naming evidence.
type speakerEvidenceJSON struct {
	Kind  string `json:"kind"`
	Quote string `json:"quote"`
	AtMS  int64  `json:"at_ms"`
}

// ListVideoSpeakers returns the stored speaker mapping of a video, ordered by
// label. A video with no mapping, like an unparseable id, yields an empty list:
// neither can name a speaker.
func (s *Store) ListVideoSpeakers(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error) {
	uid, err := uuid.Parse(videoID)
	if err != nil {
		return []domain.VideoSpeaker{}, nil
	}
	rows, err := s.queries.ListVideoSpeakers(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("postgres: list video speakers %s: %w", videoID, err)
	}
	speakers := make([]domain.VideoSpeaker, 0, len(rows))
	for _, row := range rows {
		speaker, err := videoSpeakerFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("postgres: list video speakers %s: %w", videoID, err)
		}
		speakers = append(speakers, speaker)
	}
	return speakers, nil
}

// ReplaceProposedVideoSpeakers swaps the naming step's proposals for a video in
// one transaction: its previous rows are deleted and the new ones written. Rows
// an operator confirmed or overrode are untouched, and a new proposal for such
// a label is dropped. The transaction holds the video row lock, so overlapping
// runs apply one after the other and the last one wins whole. An unknown video
// is domain.ErrVideoNotFound.
func (s *Store) ReplaceProposedVideoSpeakers(ctx context.Context, videoID string, speakers []domain.VideoSpeaker) error {
	uid, err := uuid.Parse(videoID)
	if err != nil {
		return domain.ErrVideoNotFound
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if _, err := q.LockVideoForSpeakerNaming(ctx, uid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrVideoNotFound
			}
			return fmt.Errorf("locking video: %w", err)
		}
		if err := q.DeleteProposedVideoSpeakers(ctx, uid); err != nil {
			return fmt.Errorf("clearing previous proposals: %w", err)
		}
		for _, speaker := range speakers {
			evidence, err := encodeSpeakerEvidence(speaker.Evidence)
			if err != nil {
				return fmt.Errorf("label %s: %w", speaker.Label, err)
			}
			err = q.InsertProposedVideoSpeaker(ctx, db.InsertProposedVideoSpeakerParams{
				VideoID:     uid,
				Label:       speaker.Label,
				DisplayName: speaker.Name,
				Role:        string(speaker.Role),
				Confidence:  speaker.Confidence,
				Method:      string(speaker.Method),
				Evidence:    evidence,
			})
			if err != nil {
				return fmt.Errorf("label %s: %w", speaker.Label, err)
			}
		}
		return nil
	})
	if errors.Is(err, domain.ErrVideoNotFound) {
		return domain.ErrVideoNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: replace video speakers %s: %w", videoID, err)
	}
	return nil
}

// SetOperatorVideoSpeaker records an operator's name and role for one label and
// marks the row operator-set, so later runs of the naming step leave it alone.
func (s *Store) SetOperatorVideoSpeaker(ctx context.Context, videoID, label, name string, role domain.SpeakerRole) (domain.VideoSpeaker, error) {
	uid, err := uuid.Parse(videoID)
	if err != nil {
		return domain.VideoSpeaker{}, domain.ErrVideoNotFound
	}
	row, err := s.queries.SetOperatorVideoSpeaker(ctx, db.SetOperatorVideoSpeakerParams{
		VideoID:     uid,
		Label:       label,
		DisplayName: name,
		Role:        string(role),
	})
	if err != nil {
		return domain.VideoSpeaker{}, fmt.Errorf("postgres: set video speaker %s/%s: %w", videoID, label, err)
	}
	speaker, err := videoSpeakerFromRow(row)
	if err != nil {
		return domain.VideoSpeaker{}, fmt.Errorf("postgres: set video speaker %s/%s: %w", videoID, label, err)
	}
	return speaker, nil
}

func encodeSpeakerEvidence(evidence []domain.SpeakerEvidence) ([]byte, error) {
	wire := make([]speakerEvidenceJSON, 0, len(evidence))
	for _, ev := range evidence {
		wire = append(wire, speakerEvidenceJSON{Kind: string(ev.Kind), Quote: ev.Quote, AtMS: ev.At.Milliseconds()})
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encoding evidence: %w", err)
	}
	return raw, nil
}

func videoSpeakerFromRow(r db.VideoSpeaker) (domain.VideoSpeaker, error) {
	var wire []speakerEvidenceJSON
	if err := json.Unmarshal(r.Evidence, &wire); err != nil {
		return domain.VideoSpeaker{}, fmt.Errorf("decoding evidence of label %s: %w", r.Label, err)
	}
	evidence := make([]domain.SpeakerEvidence, 0, len(wire))
	for _, ev := range wire {
		evidence = append(evidence, domain.SpeakerEvidence{
			Kind:  domain.SpeakerEvidenceKind(ev.Kind),
			Quote: ev.Quote,
			At:    time.Duration(ev.AtMS) * time.Millisecond,
		})
	}
	return domain.VideoSpeaker{
		VideoID:     r.VideoID.String(),
		Label:       r.Label,
		Name:        r.DisplayName,
		Role:        domain.SpeakerRole(r.Role),
		Confidence:  r.Confidence,
		Method:      domain.SpeakerMethod(r.Method),
		Evidence:    evidence,
		OperatorSet: r.OperatorSet,
		UpdatedAt:   r.UpdatedAt.Time,
	}, nil
}
