package handler

// Video library and upload API.
//
// These endpoints give every video a first-class record with a durable UUID
// identity, distinct from the batch processing identity used by the routes in
// processing.go (where {id} is the SHA-256 of a video source). The two
// coexist: a video has a stable record here, and is processed under its content
// digest there. Uploads go direct to object storage via presigned URLs; the
// backend never proxies the bytes.
//
//	POST /api/videos/uploads      mint a presigned PUT and a pending record
//	POST /api/videos/{id}/confirm verify the object landed, mark the record ready
//	GET  /api/videos              list curated samples and uploads together
//	GET  /api/videos/{id}         metadata plus a presigned playback URL

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// VideoService is the slice of the video service the library endpoints consume,
// satisfied by *service.VideoService.
type VideoService interface {
	RequestUpload(ctx context.Context, req service.UploadRequest) (service.UploadTicket, error)
	Confirm(ctx context.Context, id string) (domain.Video, error)
	List(ctx context.Context) ([]domain.Video, error)
	Get(ctx context.Context, id string) (service.PlayableVideo, error)
}

// maxVideoBodyBytes bounds the upload-request body; the JSON metadata is tiny.
const maxVideoBodyBytes = 1 << 20

type uploadRequestBody struct {
	Title       string `json:"title"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// presignedJSON is the wire form of a domain.PresignedRequest: the browser
// issues Method to URL and replays every header in Headers.
type presignedJSON struct {
	URL     string              `json:"url"`
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"`
}

type uploadResponse struct {
	VideoID   string        `json:"video_id"`
	ObjectKey string        `json:"object_key"`
	Status    string        `json:"status"`
	Upload    presignedJSON `json:"upload"`
}

// videoJSON is the wire form of one domain.Video. ObjectKey is intentionally
// omitted: clients address a video by id and never need the storage key.
type videoJSON struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Kind        string    `json:"kind"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type listVideosResponse struct {
	Videos []videoJSON `json:"videos"`
}

type videoResponse struct {
	videoJSON
	Playback presignedJSON `json:"playback"`
}

func requestUploadHandler(svc VideoService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body uploadRequestBody
		if !decodeJSONBody(w, r, maxVideoBodyBytes, &body) {
			return
		}

		ticket, err := svc.RequestUpload(r.Context(), service.UploadRequest{
			Title:       body.Title,
			ContentType: body.ContentType,
			SizeBytes:   body.SizeBytes,
		})
		switch {
		case errors.Is(err, service.ErrInvalidTitle):
			httpx.Error(w, http.StatusBadRequest, "title is required")
		case errors.Is(err, service.ErrInvalidContentType):
			httpx.Error(w, http.StatusUnsupportedMediaType, "unsupported content type")
		case errors.Is(err, service.ErrInvalidSize):
			httpx.Error(w, http.StatusBadRequest, "declared size is out of range")
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

func confirmVideoHandler(svc VideoService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		video, err := svc.Confirm(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case errors.Is(err, service.ErrObjectNotUploaded):
			httpx.Error(w, http.StatusConflict, "upload not found in storage")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, toVideoJSON(video))
		}
	}
}

func listVideosHandler(svc VideoService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		videos, err := svc.List(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		out := make([]videoJSON, 0, len(videos))
		for _, v := range videos {
			out = append(out, toVideoJSON(v))
		}
		httpx.JSON(w, http.StatusOK, listVideosResponse{Videos: out})
	}
}

func getVideoHandler(svc VideoService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		playable, err := svc.Get(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, videoResponse{
				videoJSON: toVideoJSON(playable.Video),
				Playback:  toPresignedJSON(playable.Playback),
			})
		}
	}
}

func toVideoJSON(v domain.Video) videoJSON {
	return videoJSON{
		ID:          v.ID,
		Title:       v.Title,
		Status:      string(v.Status),
		Kind:        string(v.Kind),
		ContentType: v.ContentType,
		SizeBytes:   v.SizeBytes,
		CreatedAt:   v.CreatedAt,
		UpdatedAt:   v.UpdatedAt,
	}
}

func toPresignedJSON(p domain.PresignedRequest) presignedJSON {
	headers := p.SignedHeaders
	if headers == nil {
		headers = map[string][]string{}
	}
	return presignedJSON{URL: p.URL, Method: p.Method, Headers: headers}
}
