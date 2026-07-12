package handler

// TV recording archive API.
//
// These endpoints are the capture worker's write path for archived hours: it
// mints a presigned PUT under the channel's recordings prefix, uploads the
// remuxed MP4 directly to storage, then registers the object so it becomes a
// kind `tv` video that replays through the ordinary watch flow. A daily prune
// enforces retention. Every route is service-only (RequireAdmin at
// registration): the worker carries the admin realm role, no browser calls
// these.
//
//	POST /api/tv/recordings/uploads mint a presigned PUT and a pending recording
//	POST /api/tv/recordings         confirm the object landed, mark it ready
//	POST /api/tv/recordings/prune   delete recordings older than a retention window

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// TVRecordingService is the slice of the recording service these endpoints
// consume, satisfied by *service.TVRecordingService.
type TVRecordingService interface {
	RequestUpload(ctx context.Context, req service.TVRecordingRequest) (service.UploadTicket, error)
	Register(ctx context.Context, videoID string) (domain.Video, error)
	Prune(ctx context.Context, retention time.Duration) (int, error)
}

type tvRecordingUploadBody struct {
	ChannelID   string    `json:"channel_id"`
	RecordedAt  time.Time `json:"recorded_at"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
}

type tvRecordingRegisterBody struct {
	VideoID string `json:"video_id"`
}

type tvRecordingPruneBody struct {
	RetentionDays int `json:"retention_days"`
}

type tvRecordingPruneResponse struct {
	Deleted int `json:"deleted"`
}

func requestTVRecordingUploadHandler(svc TVRecordingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body tvRecordingUploadBody
		if !decodeJSONBody(w, r, maxVideoBodyBytes, &body) {
			return
		}
		ticket, err := svc.RequestUpload(r.Context(), service.TVRecordingRequest{
			ChannelID:   body.ChannelID,
			RecordedAt:  body.RecordedAt,
			ContentType: body.ContentType,
			SizeBytes:   body.SizeBytes,
		})
		switch {
		case errors.Is(err, service.ErrTVRecordingNoRecordedAt):
			httpx.Error(w, http.StatusBadRequest, "recorded_at is required")
		case errors.Is(err, service.ErrTVRecordingInvalidContentType):
			httpx.Error(w, http.StatusUnsupportedMediaType, "unsupported content type")
		case errors.Is(err, service.ErrTVRecordingInvalidSize):
			httpx.Error(w, http.StatusBadRequest, "declared size is out of range")
		case errors.Is(err, domain.ErrTVChannelNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown channel")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusCreated, uploadResponse{
				VideoID:   ticket.Video.ID,
				ObjectKey: ticket.Video.ObjectKey,
				Status:    string(ticket.Video.Status),
				Upload:    toPresignedJSON(ticket.Upload),
			})
		}
	}
}

func registerTVRecordingHandler(svc TVRecordingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body tvRecordingRegisterBody
		if !decodeJSONBody(w, r, maxVideoBodyBytes, &body) {
			return
		}
		video, err := svc.Register(r.Context(), body.VideoID)
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown recording")
		case errors.Is(err, service.ErrObjectNotUploaded):
			httpx.Error(w, http.StatusConflict, "upload not found in storage")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, toVideoJSON(video))
		}
	}
}

func pruneTVRecordingsHandler(svc TVRecordingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body tvRecordingPruneBody
		if !decodeJSONBody(w, r, maxVideoBodyBytes, &body) {
			return
		}
		if body.RetentionDays <= 0 {
			httpx.Error(w, http.StatusBadRequest, "retention_days must be positive")
			return
		}
		deleted, err := svc.Prune(r.Context(), time.Duration(body.RetentionDays)*24*time.Hour)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, http.StatusOK, tvRecordingPruneResponse{Deleted: deleted})
	}
}
