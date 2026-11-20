package handler

// Recap API: an analyzed video presented as a report.
//
//	GET /api/videos/{id}/recap  the recap of a video whose analysis is complete
//
// The recap is the single projection behind the recap page and its exports:
// per-speaker speaking time, turns and outcome counts, the sourced statements
// of each speaker, and the totals. It is computed on read from the stored
// analysis and the speaker mapping.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// VideoRecapService is the recap read the endpoint consumes, satisfied by
// *service.RecapReader.
type VideoRecapService interface {
	Get(ctx context.Context, videoID string) (service.Recap, error)
}

type recapCountsJSON struct {
	Detected     int `json:"detected"`
	Credible     int `json:"credible"`
	Disputed     int `json:"disputed"`
	Unverifiable int `json:"unverifiable"`
	NotVerified  int `json:"not_verified"`
}

type recapStatementJSON struct {
	ClaimID   string          `json:"claim_id"`
	At        float64         `json:"at"`
	Verdict   string          `json:"verdict"`
	Flags     []string        `json:"flags"`
	Quote     string          `json:"quote"`
	Rationale string          `json:"rationale"`
	Sources   []domain.Source `json:"sources"`
}

// recapSpeakerJSON is one voice of the report. Times are seconds; share is
// the voice's fraction of the principals' speaking time.
type recapSpeakerJSON struct {
	Label         string               `json:"label"`
	Name          string               `json:"name"`
	Role          string               `json:"role"`
	NameConfirmed bool                 `json:"name_confirmed"`
	SpeakingTime  float64              `json:"speaking_time"`
	Turns         int                  `json:"turns"`
	Share         float64              `json:"share"`
	Counts        recapCountsJSON      `json:"counts"`
	Disputed      []recapStatementJSON `json:"disputed"`
	Credible      []recapStatementJSON `json:"credible"`
}

type recapGroupJSON struct {
	SpeakingTime float64         `json:"speaking_time"`
	Turns        int             `json:"turns"`
	Counts       recapCountsJSON `json:"counts"`
}

type videoRecapResponse struct {
	VideoID    string             `json:"video_id"`
	Title      string             `json:"title"`
	AnalyzedAt time.Time          `json:"analyzed_at,omitzero"`
	Duration   float64            `json:"duration"`
	Speakers   []recapSpeakerJSON `json:"speakers"`
	Principals recapGroupJSON     `json:"principals"`
	Others     recapGroupJSON     `json:"others"`
}

func toRecapCountsJSON(c service.RecapCounts) recapCountsJSON {
	return recapCountsJSON{
		Detected:     c.Detected,
		Credible:     c.Credible,
		Disputed:     c.Disputed,
		Unverifiable: c.Unverifiable,
		NotVerified:  c.NotVerified,
	}
}

func toRecapGroupJSON(g service.RecapGroup) recapGroupJSON {
	return recapGroupJSON{SpeakingTime: g.SpeakingTime.Seconds(), Turns: g.Turns, Counts: toRecapCountsJSON(g.Counts)}
}

func toRecapStatementsJSON(statements []service.RecapStatement) []recapStatementJSON {
	out := make([]recapStatementJSON, 0, len(statements))
	for _, s := range statements {
		flags, sources := s.Flags, s.Sources
		if flags == nil {
			flags = []string{}
		}
		if sources == nil {
			sources = []domain.Source{}
		}
		out = append(out, recapStatementJSON{
			ClaimID:   s.ClaimID,
			At:        s.At.Seconds(),
			Verdict:   s.Verdict,
			Flags:     flags,
			Quote:     s.Quote,
			Rationale: s.Rationale,
			Sources:   sources,
		})
	}
	return out
}

func toVideoRecapResponse(recap service.Recap) videoRecapResponse {
	speakers := make([]recapSpeakerJSON, 0, len(recap.Speakers))
	for _, s := range recap.Speakers {
		speakers = append(speakers, recapSpeakerJSON{
			Label:         s.Label,
			Name:          s.Name,
			Role:          string(s.Role),
			NameConfirmed: s.NameConfirmed,
			SpeakingTime:  s.SpeakingTime.Seconds(),
			Turns:         s.Turns,
			Share:         s.Share,
			Counts:        toRecapCountsJSON(s.Counts),
			Disputed:      toRecapStatementsJSON(s.Disputed),
			Credible:      toRecapStatementsJSON(s.Credible),
		})
	}
	return videoRecapResponse{
		VideoID:    recap.VideoID,
		Title:      recap.Title,
		AnalyzedAt: recap.AnalyzedAt,
		Duration:   recap.Duration.Seconds(),
		Speakers:   speakers,
		Principals: toRecapGroupJSON(recap.Principals),
		Others:     toRecapGroupJSON(recap.Others),
	}
}

// getVideoRecapHandler serves a video's recap. It is registered for any
// authenticated caller, the same access rule as the analysis read it is
// derived from. An unknown video is 404; a video with no completed, readable
// analysis is 409: there is nothing to report on yet.
func getVideoRecapHandler(svc VideoRecapService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "recap is not configured")
			return
		}
		recap, err := svc.Get(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case errors.Is(err, domain.ErrVideoAnalysisNotFound):
			httpx.Error(w, http.StatusConflict, "video has no completed analysis to recap")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, toVideoRecapResponse(recap))
		}
	}
}
