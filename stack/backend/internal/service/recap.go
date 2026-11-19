package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/speakername"
)

// RecapCounts tallies the statements of one speaker, or of a group, by
// outcome. The four outcomes are exclusive and are never folded together:
// Detected is always their sum. NotVerified is a statement the pipeline
// detected but could not judge - verification failed or was shed under load -
// as opposed to Unverifiable, which was judged and found unsupported either way.
type RecapCounts struct {
	Detected     int
	Credible     int
	Disputed     int
	Unverifiable int
	NotVerified  int
}

func (c *RecapCounts) add(o RecapCounts) {
	c.Detected += o.Detected
	c.Credible += o.Credible
	c.Disputed += o.Disputed
	c.Unverifiable += o.Unverifiable
	c.NotVerified += o.NotVerified
}

// RecapStatement is one judged statement as the report prints it. Quote is
// verbatim transcript text; Sources are the citations behind the verdict.
type RecapStatement struct {
	ClaimID   string
	At        time.Duration
	Verdict   string
	Flags     []string
	Quote     string
	Rationale string
	Sources   []domain.Source
}

// RecapSpeaker is one diarized voice in the report. Name is empty for an
// unnamed voice, which the report prints under its Label. Share is the voice's
// part of the principals' speaking time and is zero for a non-principal.
// Disputed and Credible list the sourced statements; unverifiable and
// not-verified statements appear as counts only.
type RecapSpeaker struct {
	Label         string
	Name          string
	Role          domain.SpeakerRole
	NameConfirmed bool
	SpeakingTime  time.Duration
	Turns         int
	Share         float64
	Counts        RecapCounts
	Disputed      []RecapStatement
	Credible      []RecapStatement
}

// RecapGroup sums speaking time, turns and outcome counts over a set of voices.
type RecapGroup struct {
	SpeakingTime time.Duration
	Turns        int
	Counts       RecapCounts
}

// Recap is an analyzed video presented as a report: the single projection the
// recap page renders and its exports reuse, so the two can never disagree.
// Speakers holds every voice, longest speaking time first. Principals sums the
// voices with the principal role - the ones that get a sheet - and Others the
// rest (moderators, guests, unattributed speech); together they are the whole
// analysis.
type Recap struct {
	VideoID    string
	Title      string
	AnalyzedAt time.Time
	Duration   time.Duration
	Speakers   []RecapSpeaker
	Principals RecapGroup
	Others     RecapGroup
}

// recapSpeakerSource reads the stored speaker mapping, satisfied by
// *postgres.Store.
type recapSpeakerSource interface {
	ListVideoSpeakers(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error)
}

// storedEventsSource reads the events of a stored analysis and reports a
// backend fault as an error, satisfied by *StoredAnalysisReader. The recap has
// no fallback source, so a fault must not read as "no analysis".
type storedEventsSource interface {
	StoredEvents(ctx context.Context, videoID string) ([]LiveEvent, error)
}

// RecapReader builds the recap of a video from its stored analysis and its
// speaker mapping. It computes on read and stores nothing.
type RecapReader struct {
	videos   videoRecordSource
	analyses storedEventsSource
	speakers recapSpeakerSource
}

// NewRecapReader wires a reader over the video record, the stored analysis and
// the speaker mapping.
func NewRecapReader(videos videoRecordSource, analyses storedEventsSource, speakers recapSpeakerSource) (*RecapReader, error) {
	if videos == nil {
		return nil, errors.New("service: recap reader: video store is required")
	}
	if analyses == nil {
		return nil, errors.New("service: recap reader: stored analysis source is required")
	}
	if speakers == nil {
		return nil, errors.New("service: recap reader: speaker source is required")
	}
	return &RecapReader{videos: videos, analyses: analyses, speakers: speakers}, nil
}

// Get returns the recap of a video. An unknown video is
// domain.ErrVideoNotFound; a video whose latest analysis is not complete, or
// has no readable stored result, is domain.ErrVideoAnalysisNotFound. A backend
// failure is returned as such, never as a missing analysis.
func (r *RecapReader) Get(ctx context.Context, videoID string) (Recap, error) {
	video, err := r.videos.GetVideo(ctx, videoID)
	if err != nil {
		return Recap{}, fmt.Errorf("service: recap of %q: %w", videoID, err)
	}
	if video.AnalysisStatus != domain.VideoAnalysisComplete {
		return Recap{}, domain.ErrVideoAnalysisNotFound
	}
	events, err := r.analyses.StoredEvents(ctx, videoID)
	if err != nil {
		return Recap{}, fmt.Errorf("service: recap of %q: %w", videoID, err)
	}
	speakers, err := r.speakers.ListVideoSpeakers(ctx, videoID)
	if err != nil {
		return Recap{}, fmt.Errorf("service: recap of %q: reading speakers: %w", videoID, err)
	}
	return BuildRecap(video, events, speakers), nil
}

// BuildRecap projects a stored analysis into its recap. It is pure.
//
// Speaking time and turns come from the finalized transcript. A voice's time is
// the sum of its own segments, so the silence, the clips and the unattributed
// speech between two of them are never counted as its speech, and two voices
// diarized over the same instant each keep their own time. Consecutive
// segments of one voice form one turn. Counts come from the terminal result of each claim: a later result for
// the same claim replaces the earlier one, and a claim that was announced but
// never judged counts as not verified. A voice's role and name come from the
// speaker mapping; a voice the mapping does not cover gets the role its
// speaking structure implies and no name.
func BuildRecap(video domain.Video, events []LiveEvent, mapping []domain.VideoSpeaker) Recap {
	segments := make([]domain.Segment, 0, len(events))
	var end time.Duration
	for _, ev := range events {
		end = max(end, ev.Segment.End)
		if ev.Kind == LiveEventSubtitle {
			segments = append(segments, ev.Segment)
		}
	}
	turns := speakername.Turns(segments)
	roles := speakername.Roles(turns)

	speakers := make(map[string]*RecapSpeaker)
	voice := func(label string) *RecapSpeaker {
		if s, ok := speakers[label]; ok {
			return s
		}
		s := &RecapSpeaker{Label: label, Role: cmp.Or(roles[label], domain.SpeakerRoleUnknown)}
		speakers[label] = s
		return s
	}
	for _, turn := range turns {
		voice(turn.Label).Turns++
	}
	for _, seg := range segments {
		if seg.Speaker != "" && strings.TrimSpace(seg.Text) != "" {
			voice(seg.Speaker).SpeakingTime += max(0, seg.End-seg.Start)
		}
	}
	for _, claim := range terminalClaims(events) {
		s := voice(claim.speaker)
		s.Counts.Detected++
		switch claim.statement.Verdict {
		case VerdictCredible:
			s.Counts.Credible++
			s.Credible = append(s.Credible, claim.statement)
		case VerdictDisputed:
			s.Counts.Disputed++
			s.Disputed = append(s.Disputed, claim.statement)
		case VerdictUnverifiable:
			s.Counts.Unverifiable++
		default:
			s.Counts.NotVerified++
		}
	}
	for _, m := range mapping {
		if s, ok := speakers[m.Label]; ok {
			s.Name = m.Name
			s.Role = m.Role
			s.NameConfirmed = m.OperatorSet
		}
	}

	recap := Recap{
		VideoID:    video.ID,
		Title:      video.Title,
		AnalyzedAt: video.AnalyzedAt,
		Duration:   cmp.Or(time.Duration(video.DurationMS)*time.Millisecond, end),
		Speakers:   make([]RecapSpeaker, 0, len(speakers)),
	}
	for _, s := range speakers {
		group := &recap.Others
		if s.Role == domain.SpeakerRolePrincipal {
			group = &recap.Principals
		}
		group.SpeakingTime += s.SpeakingTime
		group.Turns += s.Turns
		group.Counts.add(s.Counts)
		recap.Speakers = append(recap.Speakers, *s)
	}
	for i := range recap.Speakers {
		s := &recap.Speakers[i]
		if s.Role == domain.SpeakerRolePrincipal && recap.Principals.SpeakingTime > 0 {
			s.Share = float64(s.SpeakingTime) / float64(recap.Principals.SpeakingTime)
		}
	}
	slices.SortFunc(recap.Speakers, func(a, b RecapSpeaker) int {
		return cmp.Or(cmp.Compare(b.SpeakingTime, a.SpeakingTime), cmp.Compare(a.Label, b.Label))
	})
	return recap
}

// recapClaim is one claim at its terminal state, attributed to a voice.
type recapClaim struct {
	speaker   string
	statement RecapStatement
}

// terminalClaims reduces the event stream to one entry per claim, in the order
// the claims first appeared. The claims announcement supplies the speaking
// voice, the verbatim quote and where it was said: a unit groups several
// transcript segments, so the time is that of the segment the quote sits in,
// and a claim the decomposer could not anchor is quoted with the whole unit
// rather than with its first sentence alone. Each result for the claim then overwrites its
// verdict, so the last verdict stands. A result that carries no verdict
// (unchecked, error) never erases an earlier one, exactly as the stored
// analysis counters count, so the recap and those counters always agree.
func terminalClaims(events []LiveEvent) []recapClaim {
	index := make(map[string]int)
	var claims []recapClaim
	at := func(id string) *recapClaim {
		i, ok := index[id]
		if !ok {
			i = len(claims)
			index[id] = i
			claims = append(claims, recapClaim{statement: RecapStatement{ClaimID: id}})
		}
		return &claims[i]
	}
	subtitles := make(map[string]domain.Segment)
	for _, ev := range events {
		switch ev.Kind {
		case LiveEventSubtitle:
			subtitles[ev.ID] = ev.Segment
		case LiveEventClaims:
			unitText := unitTranscript(ev, subtitles)
			for _, c := range ev.Claims {
				claim := at(c.ClaimID)
				claim.speaker = ev.Segment.Speaker
				claim.statement.At = ev.Segment.Start
				if len(c.Spans) > 0 {
					if seg, ok := subtitles[c.Spans[0].SegmentID]; ok {
						claim.statement.At = seg.Start
					}
				}
				claim.statement.Quote = cmp.Or(c.Quote, unitText)
			}
		case LiveEventResult:
			if ev.ClaimID == "" || ev.ClaimStatus == ClaimStatusPending || ev.ClaimStatus == ClaimStatusChecking {
				continue
			}
			claim := at(ev.ClaimID)
			if claim.statement.Quote == "" {
				claim.speaker = ev.Segment.Speaker
				claim.statement.At = ev.Segment.Start
				claim.statement.Quote = ev.Segment.Text
			}
			if v := ev.Verdict; v != nil {
				claim.statement.Verdict = v.Verdict
				claim.statement.Flags = v.Flags
				claim.statement.Rationale = v.Rationale
				claim.statement.Sources = citedSources(v.Citations)
			}
		}
	}
	return claims
}

// unitTranscript is the verbatim text of a claims unit: its member segments
// joined in order, or the announcing segment's own text when the members are
// not in the stream.
func unitTranscript(ev LiveEvent, subtitles map[string]domain.Segment) string {
	parts := make([]string, 0, len(ev.SegmentIDs))
	for _, id := range ev.SegmentIDs {
		if seg, ok := subtitles[id]; ok {
			parts = append(parts, strings.TrimSpace(seg.Text))
		}
	}
	if len(parts) == 0 {
		return ev.Segment.Text
	}
	return strings.Join(parts, " ")
}

// citedSources flattens the citations behind a verdict into distinct sources,
// in citation order. A source with no URL cannot be linked and is dropped.
func citedSources(citations []domain.SegmentMatch) []domain.Source {
	var sources []domain.Source
	for _, c := range citations {
		for _, s := range c.Sources {
			if s.URL == "" || slices.ContainsFunc(sources, func(o domain.Source) bool { return o.URL == s.URL }) {
				continue
			}
			sources = append(sources, s)
		}
	}
	return sources
}
