package handler

// Speaker naming API: the mapping from a video's diarized labels to people.
//
//	GET   /api/videos/{id}/speakers          the stored mapping
//	POST  /api/videos/{id}/speakers/name     (admin) re-run the naming step
//	PATCH /api/videos/{id}/speakers/{label}  (admin) name, set the role of, and
//	                                         confirm one speaker
//
// The mapping is also embedded in the analysis read and applied to the SRT and
// CSV exports, so a consumer of those never needs this endpoint to show names.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// speakerNamingDeadline bounds a synchronous re-run of the naming step. It
// extends the server's write deadline for that one response: the step makes a
// few model calls over a long transcript and outlasts the default. It stays
// under the 60 s idle timeout of the hops in front of the backend (the load
// balancer, the local-dev proxy), so a slow run ends as the handler's own 502
// rather than a connection one of them dropped.
const speakerNamingDeadline = 55 * time.Second

// maxSpeakerUpdateBytes bounds a speaker update body.
const maxSpeakerUpdateBytes = 4 << 10

// VideoSpeakerService is the speaker naming surface the endpoints consume,
// satisfied by *service.SpeakerNamer.
type VideoSpeakerService interface {
	List(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error)
	Name(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error)
	Update(ctx context.Context, videoID, label string, update service.SpeakerUpdate) (domain.VideoSpeaker, error)
}

// speakerLister is the read slice of the speaker mapping the analysis read and
// the exports consume.
type speakerLister interface {
	List(ctx context.Context, videoID string) ([]domain.VideoSpeaker, error)
}

type speakerEvidenceJSON struct {
	Kind  string  `json:"kind"`
	Quote string  `json:"quote"`
	At    float64 `json:"at"`
}

// videoSpeakerJSON is the wire form of one speaker. Name is empty for a label
// left unnamed; At is the evidence turn's video time in seconds.
type videoSpeakerJSON struct {
	Label       string                `json:"label"`
	Name        string                `json:"name"`
	Role        string                `json:"role"`
	Confidence  float64               `json:"confidence"`
	Method      string                `json:"method"`
	Evidence    []speakerEvidenceJSON `json:"evidence"`
	OperatorSet bool                  `json:"operator_set"`
}

type videoSpeakersResponse struct {
	Speakers []videoSpeakerJSON `json:"speakers"`
}

type speakerUpdateRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

func toVideoSpeakerJSON(s domain.VideoSpeaker) videoSpeakerJSON {
	evidence := make([]speakerEvidenceJSON, 0, len(s.Evidence))
	for _, ev := range s.Evidence {
		evidence = append(evidence, speakerEvidenceJSON{Kind: string(ev.Kind), Quote: ev.Quote, At: ev.At.Seconds()})
	}
	return videoSpeakerJSON{
		Label:       s.Label,
		Name:        s.Name,
		Role:        string(s.Role),
		Confidence:  s.Confidence,
		Method:      string(s.Method),
		Evidence:    evidence,
		OperatorSet: s.OperatorSet,
	}
}

func toVideoSpeakersJSON(speakers []domain.VideoSpeaker) []videoSpeakerJSON {
	out := make([]videoSpeakerJSON, 0, len(speakers))
	for _, s := range speakers {
		out = append(out, toVideoSpeakerJSON(s))
	}
	return out
}

// speakerNames returns the label-to-name map of a video's named speakers, for
// the exports. Names are an enrichment: with no speaker service, or when the
// mapping cannot be read, the result is empty and the caller falls back to the
// diarized labels.
func speakerNames(ctx context.Context, speakers speakerLister, videoID string) map[string]string {
	if speakers == nil {
		return nil
	}
	list, err := speakers.List(ctx, videoID)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(list))
	for _, s := range list {
		if s.Name != "" {
			names[s.Label] = s.Name
		}
	}
	return names
}

// listVideoSpeakersHandler serves a video's speaker mapping to any
// authenticated caller, like the analysis it annotates. A video the naming
// step has not run on yields an empty list.
func listVideoSpeakersHandler(svc VideoSpeakerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "speaker naming is not configured")
			return
		}
		speakers, err := svc.List(r.Context(), r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, http.StatusOK, videoSpeakersResponse{Speakers: toVideoSpeakersJSON(speakers)})
	}
}

// nameVideoSpeakersHandler re-runs the naming step on a video's stored
// analysis and returns the resulting mapping. An unknown video is 404, a video
// with no stored analysis 409, and a step that could not complete 502: the
// previous mapping is left in place.
func nameVideoSpeakersHandler(svc VideoSpeakerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "speaker naming is not configured")
			return
		}
		// Best effort: a writer that cannot move its deadline keeps the server
		// default, and a slow run then ends as a dropped response, not a hang.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(speakerNamingDeadline))
		ctx, cancel := context.WithTimeout(r.Context(), speakerNamingDeadline)
		defer cancel()

		speakers, err := svc.Name(ctx, r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case errors.Is(err, domain.ErrVideoAnalysisNotFound):
			httpx.Error(w, http.StatusConflict, "video has no stored analysis to name speakers from")
		case err != nil:
			httpx.Error(w, http.StatusBadGateway, "speaker naming failed; the previous names are unchanged")
		default:
			httpx.JSON(w, http.StatusOK, videoSpeakersResponse{Speakers: toVideoSpeakersJSON(speakers)})
		}
	}
}

// updateVideoSpeakerHandler records an operator's name and role for one
// diarized label and marks it confirmed.
func updateVideoSpeakerHandler(svc VideoSpeakerService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "speaker naming is not configured")
			return
		}
		var req speakerUpdateRequest
		if !decodeJSONBody(w, r, maxSpeakerUpdateBytes, &req) {
			return
		}
		speaker, err := svc.Update(r.Context(), r.PathValue("id"), r.PathValue("label"), service.SpeakerUpdate{
			Name: req.Name,
			Role: domain.SpeakerRole(req.Role),
		})
		switch {
		case errors.Is(err, domain.ErrInvalidSpeakerRole):
			httpx.Error(w, http.StatusBadRequest, "role must be one of principal, moderator, guest, unknown")
		case errors.Is(err, domain.ErrInvalidSpeakerName):
			httpx.Error(w, http.StatusBadRequest, "name is too long")
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case errors.Is(err, domain.ErrVideoSpeakerNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown speaker")
		case errors.Is(err, domain.ErrVideoAnalysisNotFound):
			httpx.Error(w, http.StatusConflict, "video has no stored analysis")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, toVideoSpeakerJSON(speaker))
		}
	}
}
