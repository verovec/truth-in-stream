package handler

// Durable video analysis API.
//
//	POST /api/videos/{id}/analyse   (admin) start or re-run the headless
//	                                server-side pre-analysis of a ready video
//	GET  /api/videos/{id}/analysis  the video's pre-analysis lifecycle and,
//	                                when complete, the stored frame list
//
// The GET endpoint is the poll target while a pre-analysis runs and the
// hydration source once it completes: frames carries the whole session in the
// exact wire shapes the live WebSocket emits (shared serializer, see
// toLiveFrame), with absolute video-time timestamps, so the frontend replays
// it through its existing frame reducers without a socket or audio capture.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// VideoAnalysisService is the slice of the stored-analysis read side the
// analysis endpoint consumes, satisfied by *service.StoredAnalysisReader.
type VideoAnalysisService interface {
	Get(ctx context.Context, videoID string) (service.VideoAnalysisView, error)
}

// VideoAnalysisStarter is the slice of the headless pre-analysis job the
// trigger endpoint drives, satisfied by *service.VideoAnalyzer. Start claims
// the video under the analysing lock and returns synchronously with a
// classified refusal, so the handler owns nothing but the status mapping.
type VideoAnalysisStarter interface {
	Start(ctx context.Context, id string) error
}

// analyseVideoHandler starts (or re-runs, from complete or failed) a video's
// headless pre-analysis and returns 202; the run proceeds in the background
// and the analysis endpoint above is the poll target. An unknown video is 404,
// a video whose upload is not ready is 422 (there is no media to analyse yet),
// and a video already analysing is 409 (the analysing status is the job lock).
func analyseVideoHandler(analyzer VideoAnalysisStarter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := analyzer.Start(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case errors.Is(err, domain.ErrVideoNotReady):
			httpx.Error(w, http.StatusUnprocessableEntity, "video is not ready for analysis")
		case errors.Is(err, domain.ErrVideoAnalysisInProgress):
			httpx.Error(w, http.StatusConflict, "analysis is already in progress")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}
}

// analysisCountersJSON is the stored result's denormalized claim counters, for
// badges without loading the frames.
type analysisCountersJSON struct {
	Total        int `json:"total"`
	Credible     int `json:"credible"`
	Disputed     int `json:"disputed"`
	Unverifiable int `json:"unverifiable"`
}

// videoAnalysisResponse is the wire form of a video's analysis state. The
// lifecycle fields are always present; engine, counters, and frames appear
// only once a completed run's stored result is readable, so a client keys
// "hydratable" on frames being present rather than on status alone.
type videoAnalysisResponse struct {
	AnalysisStatus     string                `json:"analysis_status"`
	AnalysisError      string                `json:"analysis_error,omitzero"`
	AnalyzedAt         time.Time             `json:"analyzed_at,omitzero"`
	AnalysisRuns       int                   `json:"analysis_runs"`
	AnalysisProgressMS int64                 `json:"analysis_progress_ms"`
	Engine             json.RawMessage       `json:"engine,omitempty"`
	Counters           *analysisCountersJSON `json:"counters,omitempty"`
	Frames             []any                 `json:"frames,omitempty"`
}

// getVideoAnalysisHandler serves a video's analysis lifecycle and stored
// frames. It is registered for any authenticated caller: analyzed playback is
// a consumption read, like the video record itself. The per-claim evidence
// detail inside claim_result frames is gated exactly as it is on the live
// socket - the debug flag AND a verified admin claim - so a cached frame is
// shaped identically to its live original for the same caller.
func getVideoAnalysisHandler(svc VideoAnalysisService, debugFactCheckEnabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		view, err := svc.Get(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
			return
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}

		resp := videoAnalysisResponse{
			AnalysisStatus:     string(view.Video.AnalysisStatus),
			AnalysisError:      view.Video.AnalysisError,
			AnalyzedAt:         view.Video.AnalyzedAt,
			AnalysisRuns:       view.Video.AnalysisRuns,
			AnalysisProgressMS: view.Video.AnalysisProgressMS,
		}
		if view.Analysis != nil {
			resp.Engine = json.RawMessage(view.Analysis.Engine)
			resp.Counters = &analysisCountersJSON{
				Total:        view.Analysis.ClaimsTotal,
				Credible:     view.Analysis.ClaimsCredible,
				Disputed:     view.Analysis.ClaimsDisputed,
				Unverifiable: view.Analysis.ClaimsUnverifiable,
			}
		}
		if len(view.Events) > 0 {
			debugFactCheck := debugFactCheckEnabled && middleware.IdentityFrom(r.Context()).IsAdmin()
			frames := make([]any, 0, len(view.Events))
			for _, ev := range view.Events {
				if frame := toLiveFrame(ev, debugFactCheck); frame != nil {
					frames = append(frames, frame)
				}
			}
			resp.Frames = frames
		}
		httpx.JSON(w, http.StatusOK, resp)
	}
}
