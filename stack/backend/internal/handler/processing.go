package handler

// Video processing API.
//
// Three endpoints drive the batch fact-check pipeline:
//
//	POST /api/videos                  submit a video source for processing
//	GET  /api/videos/{id}/status      poll progress (segments done out of total)
//	GET  /api/videos/{id}/results     fetch the finished, timestamp-indexed results
//
// Submit accepts {"source": "<video source identifier>"} and answers
// {"video_id": "...", "status": "processing" | "complete"} - 202 when a run
// was started or is in flight, 200 when cached results already exist.
//
// Status answers {"video_id", "status", "segments_total", "segments_done"}
// plus "error" when status is "failed". Results are only served once status
// is "complete"; a known but unfinished video answers 409 so partial results
// are never mistaken for complete ones.
//
// Stable client contract - the per-segment result shape. Results answer:
//
//	{
//	  "video_id": "<sha-256 of the source>",
//	  "segments": [
//	    {
//	      "start": 12.48,                 // seconds from video start
//	      "end": 15.2,                    // seconds from video start
//	      "text": "what was said",
//	      "matches": [                    // ranked, most similar first
//	        {
//	          "claim": "verified claim text",
//	          "verdict": "corroborates" | "contradicts" | "unclear",
//	          "sources": [{"title": "...", "url": "https://..."}],
//	          "similarity": 0.92          // higher is more similar
//	        }
//	      ],
//	      "skip_reason": "not_a_claim" | "not_covered"  // only when skipped
//	    }
//	  ]
//	}
//
// A segment carries skip_reason only when the check-worthiness gate declined to
// check it: the segment is not a verifiable claim ("not_a_claim") or the corpus
// does not cover it ("not_covered"). When skip_reason is absent the segment was
// checked and matches is authoritative - an empty matches then means "checked,
// no confident match", which is distinct from "not checked".
//
// Segments are ordered by start time and keyed by it. This object is exactly
// what the future live mode will emit incrementally per segment, so clients
// written against batch results work unchanged against a live stream.

import (
	"context"
	"errors"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// ProcessingService is the slice of the processing service the video
// endpoints consume, satisfied by *service.Processor.
type ProcessingService interface {
	Submit(ctx context.Context, source string) (service.Submission, error)
	Progress(ctx context.Context, videoID string) (service.Progress, error)
	Results(ctx context.Context, videoID string) ([]domain.SegmentResult, error)
}

type submitRequest struct {
	Source string `json:"source"`
}

type submitResponse struct {
	VideoID string `json:"video_id"`
	Status  string `json:"status"`
}

type statusResponse struct {
	VideoID       string `json:"video_id"`
	Status        string `json:"status"`
	SegmentsTotal int    `json:"segments_total"`
	SegmentsDone  int    `json:"segments_done"`
	Error         string `json:"error,omitempty"`
}

type resultsResponse struct {
	VideoID  string        `json:"video_id"`
	Segments []segmentJSON `json:"segments"`
}

// segmentJSON is the wire form of one domain.SegmentResult: timestamps as
// seconds, matches served verbatim from their persisted shape. SkipReason is
// present only when the gate skipped the segment; its absence means the segment
// was checked and Matches (possibly empty) is authoritative.
type segmentJSON struct {
	Start      float64               `json:"start"`
	End        float64               `json:"end"`
	Text       string                `json:"text"`
	Matches    []domain.SegmentMatch `json:"matches"`
	SkipReason string                `json:"skip_reason,omitempty"`
}

// maxSubmitBodyBytes bounds the submit request body; a video source
// identifier fits comfortably within it.
const maxSubmitBodyBytes = 1 << 20

func submitVideoHandler(svc ProcessingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req submitRequest
		if !decodeJSONBody(w, r, maxSubmitBodyBytes, &req) {
			return
		}

		sub, err := svc.Submit(r.Context(), req.Source)
		switch {
		case errors.Is(err, service.ErrEmptySource):
			httpx.Error(w, http.StatusBadRequest, "source is required")
		case errors.Is(err, service.ErrQueueFull):
			httpx.Error(w, http.StatusServiceUnavailable, "processing queue is full, retry later")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		case sub.Status == service.StatusComplete:
			httpx.JSON(w, http.StatusOK, submitResponse{VideoID: sub.VideoID, Status: string(sub.Status)})
		default:
			httpx.JSON(w, http.StatusAccepted, submitResponse{VideoID: sub.VideoID, Status: string(sub.Status)})
		}
	}
}

func videoStatusHandler(svc ProcessingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prog, err := svc.Progress(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, service.ErrUnknownVideo):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, statusResponse{
				VideoID:       prog.VideoID,
				Status:        string(prog.Status),
				SegmentsTotal: prog.SegmentsTotal,
				SegmentsDone:  prog.SegmentsDone,
				Error:         prog.Err,
			})
		}
	}
}

func videoResultsHandler(svc ProcessingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		videoID := r.PathValue("id")
		results, err := svc.Results(r.Context(), videoID)
		switch {
		case errors.Is(err, service.ErrUnknownVideo):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case errors.Is(err, service.ErrResultsNotReady):
			httpx.Error(w, http.StatusConflict, "processing has not completed")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, toResultsResponse(videoID, results))
		}
	}
}

func toResultsResponse(videoID string, results []domain.SegmentResult) resultsResponse {
	segments := make([]segmentJSON, 0, len(results))
	for _, r := range results {
		matches := r.Matches
		if matches == nil {
			matches = []domain.SegmentMatch{}
		}
		segments = append(segments, segmentJSON{
			Start:      r.Start.Seconds(),
			End:        r.End.Seconds(),
			Text:       r.Text,
			Matches:    matches,
			SkipReason: string(r.SkipReason),
		})
	}
	return resultsResponse{VideoID: videoID, Segments: segments}
}
