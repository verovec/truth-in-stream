package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/speakername"
)

// maxSpeakerNameRunes bounds an operator-entered speaker name.
const maxSpeakerNameRunes = 120

// videoSpeakerStore is the persistence the speaker naming step needs,
// satisfied by *postgres.Store.
type videoSpeakerStore interface {
	ListVideoSpeakers(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error)
	ReplaceProposedVideoSpeakers(ctx context.Context, videoID string, speakers []domain.VideoSpeaker) error
	SetOperatorVideoSpeaker(ctx context.Context, videoID, label, name string, role domain.SpeakerRole) (domain.VideoSpeaker, error)
}

// storedSnapshotSource reads a video's stored analysis events, satisfied by
// *StoredAnalysisReader.
type storedSnapshotSource interface {
	Snapshot(ctx context.Context, videoID string) ([]LiveEvent, bool, error)
}

// SpeakerNameModel is the generative edge of speaker naming, satisfied by
// *speakername.Model. Its answers are untrusted: SpeakerNamer validates every
// one against the transcript.
type SpeakerNameModel interface {
	Participants(ctx context.Context, title string, turns []speakername.Turn) ([]speakername.Participant, error)
	BreakTie(ctx context.Context, label string, contenders []string, turns []speakername.Turn) (name, quote string, err error)
}

// SpeakerUpdate is an operator's decision for one diarized label. Saving it
// confirms the mapping: later runs of the naming step never overwrite it.
type SpeakerUpdate struct {
	Name string
	Role domain.SpeakerRole
}

// SpeakerNamer names the diarized speakers of an analyzed video. It reads the
// stored analysis and never rewrites it: names live in their own mapping and
// are applied where an analysis is read.
type SpeakerNamer struct {
	videos    videoRecordSource
	snapshots storedSnapshotSource
	store     videoSpeakerStore
	model     SpeakerNameModel
	logger    *slog.Logger
}

// NewSpeakerNamer wires a namer. model may be nil (no generative provider
// configured): the step then still classifies roles and leaves every label
// unnamed.
func NewSpeakerNamer(videos videoRecordSource, snapshots storedSnapshotSource, store videoSpeakerStore, model SpeakerNameModel, logger *slog.Logger) (*SpeakerNamer, error) {
	if videos == nil {
		return nil, errors.New("service: speaker namer: video store is required")
	}
	if snapshots == nil {
		return nil, errors.New("service: speaker namer: snapshot source is required")
	}
	if store == nil {
		return nil, errors.New("service: speaker namer: speaker store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SpeakerNamer{videos: videos, snapshots: snapshots, store: store, model: model, logger: logger}, nil
}

// Run names the speakers of a video as a post-analysis step.
func (n *SpeakerNamer) Run(ctx context.Context, videoID string) error {
	_, err := n.Name(ctx, videoID)
	return err
}

// Name runs the naming step on a video's stored analysis and returns the
// resulting mapping. It can be re-run at will: its previous proposals are
// replaced, operator-set speakers are kept. An unknown video is
// domain.ErrVideoNotFound and a video with no stored analysis
// domain.ErrVideoAnalysisNotFound. On any failure nothing is written, so the
// previous mapping - or none - stays in place.
func (n *SpeakerNamer) Name(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error) {
	video, err := n.videos.GetVideo(ctx, videoID)
	if err != nil {
		return nil, fmt.Errorf("service: naming speakers of %q: %w", videoID, err)
	}
	turns, err := n.turns(ctx, videoID)
	if err != nil {
		return nil, err
	}

	names, err := n.participantNames(ctx, video.Title, turns)
	if err != nil {
		return nil, fmt.Errorf("service: naming speakers of %q: %w", videoID, err)
	}
	proposals := speakername.Resolve(turns, names)
	undecided := n.breakTies(ctx, videoID, proposals, turns)
	n.keepEarlierTieBreaks(ctx, videoID, proposals, undecided)

	speakers := make([]domain.VideoSpeaker, 0, len(proposals))
	for _, p := range proposals {
		speakers = append(speakers, domain.VideoSpeaker{
			VideoID:    videoID,
			Label:      p.Label,
			Name:       p.Name,
			Role:       p.Role,
			Confidence: p.Confidence,
			Method:     p.Method,
			Evidence:   p.Evidence,
		})
	}
	if err := n.store.ReplaceProposedVideoSpeakers(ctx, videoID, speakers); err != nil {
		return nil, fmt.Errorf("service: naming speakers of %q: %w", videoID, err)
	}
	return n.List(ctx, videoID)
}

// List returns the stored speaker mapping of a video, empty when the naming
// step has not produced one.
func (n *SpeakerNamer) List(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error) {
	speakers, err := n.store.ListVideoSpeakers(ctx, videoID)
	if err != nil {
		return nil, fmt.Errorf("service: listing speakers of %q: %w", videoID, err)
	}
	return speakers, nil
}

// Update records an operator's name and role for one label. The label must be
// a voice of the video's stored analysis (domain.ErrVideoSpeakerNotFound
// otherwise); the role must be a known one (domain.ErrInvalidSpeakerRole) and
// the name within bounds (domain.ErrInvalidSpeakerName). An empty name is a
// valid decision: the operator confirms the voice stays unnamed.
func (n *SpeakerNamer) Update(ctx context.Context, videoID, label string, update SpeakerUpdate) (domain.VideoSpeaker, error) {
	name := strings.TrimSpace(update.Name)
	if !update.Role.Valid() {
		return domain.VideoSpeaker{}, domain.ErrInvalidSpeakerRole
	}
	if utf8.RuneCountInString(name) > maxSpeakerNameRunes {
		return domain.VideoSpeaker{}, domain.ErrInvalidSpeakerName
	}
	if _, err := n.videos.GetVideo(ctx, videoID); err != nil {
		return domain.VideoSpeaker{}, fmt.Errorf("service: updating speaker of %q: %w", videoID, err)
	}
	turns, err := n.turns(ctx, videoID)
	if err != nil {
		return domain.VideoSpeaker{}, err
	}
	if !slices.ContainsFunc(turns, func(t speakername.Turn) bool { return t.Label == label }) {
		return domain.VideoSpeaker{}, domain.ErrVideoSpeakerNotFound
	}
	speaker, err := n.store.SetOperatorVideoSpeaker(ctx, videoID, label, name, update.Role)
	if err != nil {
		return domain.VideoSpeaker{}, fmt.Errorf("service: updating speaker %s of %q: %w", label, videoID, err)
	}
	return speaker, nil
}

// turns rebuilds the speaker turns of a video from the finalized transcript
// events of its stored analysis.
func (n *SpeakerNamer) turns(ctx context.Context, videoID string) ([]speakername.Turn, error) {
	events, found, err := n.snapshots.Snapshot(ctx, videoID)
	if err != nil {
		return nil, fmt.Errorf("service: reading stored analysis of %q: %w", videoID, err)
	}
	if !found {
		return nil, domain.ErrVideoAnalysisNotFound
	}
	segments := make([]domain.Segment, 0, len(events))
	for _, ev := range events {
		if ev.Kind == LiveEventSubtitle {
			segments = append(segments, ev.Segment)
		}
	}
	return speakername.Turns(segments), nil
}

// participantNames asks the model for the participants and keeps only those
// the transcript or the title grounds. With no model there are no names.
func (n *SpeakerNamer) participantNames(ctx context.Context, title string, turns []speakername.Turn) ([]string, error) {
	if n.model == nil || len(turns) == 0 {
		return nil, nil
	}
	participants, err := n.model.Participants(ctx, title, turns)
	if err != nil {
		return nil, err
	}
	return speakername.Ground(participants, title, turns), nil
}

// breakTies asks the model to settle each label the signals left open between
// a few contenders. A failed or rejected answer leaves the label unnamed: a
// tie-break is an optional refinement, never a reason to fail the step. It
// returns the labels whose call failed outright, as opposed to answered and
// declined: for those the step learned nothing new.
func (n *SpeakerNamer) breakTies(ctx context.Context, videoID string, proposals []speakername.Proposal, turns []speakername.Turn) []string {
	if n.model == nil {
		return nil
	}
	var failed []string
	for i := range proposals {
		label, contenders := proposals[i].Label, proposals[i].Contenders
		if len(contenders) == 0 {
			continue
		}
		name, quote, err := n.model.BreakTie(ctx, label, contenders, turns)
		if err != nil {
			n.logger.WarnContext(ctx, "speaker tie-break failed",
				slog.String("video_id", videoID), slog.String("label", label), slog.Any("err", err))
			failed = append(failed, label)
			continue
		}
		if name == "" {
			continue
		}
		if !speakername.ApplyTieBreak(proposals, turns, label, name, quote) {
			n.logger.InfoContext(ctx, "speaker tie-break rejected: not grounded in the transcript",
				slog.String("video_id", videoID), slog.String("label", label))
		}
	}
	return failed
}

// keepEarlierTieBreaks carries forward the name an earlier run settled by
// tie-break for each label whose tie-break call failed this time, so a
// transient provider error on a re-run never erases a name the same analysis
// already earned. A name another label now holds is not restored. The stored
// mapping is cleared whenever a new analysis is persisted, so nothing is ever
// carried across a re-analysis.
func (n *SpeakerNamer) keepEarlierTieBreaks(ctx context.Context, videoID string, proposals []speakername.Proposal, labels []string) {
	if len(labels) == 0 {
		return
	}
	stored, err := n.store.ListVideoSpeakers(ctx, videoID)
	if err != nil {
		n.logger.WarnContext(ctx, "reading earlier speaker names failed; open ties stay unnamed",
			slog.String("video_id", videoID), slog.Any("err", err))
		return
	}
	for i := range proposals {
		p := &proposals[i]
		if p.Name != "" || !slices.Contains(labels, p.Label) {
			continue
		}
		earlier := slices.IndexFunc(stored, func(s domain.VideoSpeaker) bool {
			return s.Label == p.Label && s.Method == domain.SpeakerMethodTieBreak && s.Name != ""
		})
		if earlier < 0 || slices.ContainsFunc(proposals, func(o speakername.Proposal) bool { return o.Name == stored[earlier].Name }) {
			continue
		}
		p.Name = stored[earlier].Name
		p.Confidence = stored[earlier].Confidence
		p.Method = domain.SpeakerMethodTieBreak
		p.Evidence = stored[earlier].Evidence
	}
}
