package handler

// TV recording archive API.
//
// These endpoints are the capture worker's write path for archived hours: it
// mints a presigned PUT under the channel's recordings prefix, uploads the
// remuxed MP4 directly to storage, then registers the object so it becomes a
// kind `tv` video that replays through the ordinary watch flow. A daily prune
// enforces retention. Every route is service-only, gated by RequireCaptureService
// at registration (the worker's scoped tv-capture role, or an admin) - not
// blanket admin; no browser calls these.
//
//	POST /api/tv/recordings/uploads mint a presigned PUT and a pending recording
//	POST /api/tv/recordings         confirm the object landed, mark it ready
//	POST /api/tv/recordings/prune   delete recordings older than a retention window
//
// One read path is a consumption endpoint, not a worker write, so it serves any
// authenticated user (the /tv page's recordings strip) rather than the capture
// service role:
//
//	GET  /api/tv/channels/{id}/recordings  list a channel's ready recordings

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
	ListRecordings(ctx context.Context, channelID string) ([]domain.Video, error)
}

// tvRecordingJSON is the wire form of one recording in the /tv strip. It is a
// deliberately thin projection of a kind `tv` video: the fields the strip needs
// to render a row and open the player by id, not the full video record (no
// object key, presign, or channel linkage). RecordedAt is RFC3339; DurationMS is
// omitted when unknown so a still-unprobed capture carries no misleading zero.
type tvRecordingJSON struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	RecordedAt string `json:"recorded_at"`
	DurationMS int64  `json:"duration_ms,omitzero"`
	Status     string `json:"status"`
}

type listTVRecordingsResponse struct {
	Recordings []tvRecordingJSON `json:"recordings"`
}

// listTVRecordingsHandler lists one channel's ready archived recordings, newest
// first, for the /tv page. It is a consumption read served to any authenticated
// user (registered without the capture-service gate), mirroring the channel
// list. An unknown or malformed channel id yields an empty list, not a 404: the
// strip renders "no recordings" identically either way.
func listTVRecordingsHandler(svc TVRecordingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recordings, err := svc.ListRecordings(r.Context(), r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		out := make([]tvRecordingJSON, 0, len(recordings))
		for _, v := range recordings {
			out = append(out, tvRecordingJSON{
				ID:         v.ID,
				Title:      v.Title,
				RecordedAt: v.RecordedAt.UTC().Format(time.RFC3339),
				DurationMS: v.DurationMS,
				Status:     string(v.Status),
			})
		}
		httpx.JSON(w, http.StatusOK, listTVRecordingsResponse{Recordings: out})
	}
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
