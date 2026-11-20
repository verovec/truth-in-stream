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
// Ingestion mutations (upload, confirm, YouTube import) and delete are
// admin-only, gated by middleware.RequireAdmin at registration; the reads serve
// any authenticated user. Video ingestion is a backoffice operation while
// playback and live analysis stay open to every signed-in caller.
//
//	POST   /api/videos/uploads      mint a presigned PUT and a pending record (admin)
//	POST   /api/videos/{id}/confirm verify the object landed, mark the record ready (admin)
//	POST   /api/videos/youtube      ingest a video from a YouTube link (202, async) (admin)
//	DELETE /api/videos/{id}         remove a record and its media object (admin)
//	GET    /api/videos              list curated samples and uploads together
//	GET    /api/videos/{id}         metadata plus a presigned playback URL

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
	Delete(ctx context.Context, id string) error
}

// YouTubeService is the slice of the ingest service the YouTube endpoint
// consumes, satisfied by *service.IngestService. Submit returns immediately with
// a pending record; the download runs in the background.
type YouTubeService interface {
	Submit(ctx context.Context, url string) (domain.Video, error)
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
// SourceURL, DurationMS, and Error are only meaningful for ingested videos and
// are omitted when zero, so an upload's wire form is unchanged. AnalysisStatus
// rides on every list item so tiles and the backoffice badge pre-analysis
// state without a per-row call; AnalyzedAt dates a completed analysis and is
// omitted until one exists.
type videoJSON struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	Kind           string    `json:"kind"`
	ContentType    string    `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	SourceURL      string    `json:"source_url,omitzero"`
	DurationMS     int64     `json:"duration_ms,omitzero"`
	Error          string    `json:"error,omitzero"`
	AnalysisStatus string    `json:"analysis_status"`
	AnalyzedAt     time.Time `json:"analyzed_at,omitzero"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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

type ingestYouTubeBody struct {
	URL string `json:"url"`
}

// ingestYouTubeHandler accepts a YouTube link and returns 202 with the pending
// record; the download proceeds in the background. An unparseable link is 400.
func ingestYouTubeHandler(svc YouTubeService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body ingestYouTubeBody
		if !decodeJSONBody(w, r, maxVideoBodyBytes, &body) {
			return
		}

		video, err := svc.Submit(r.Context(), body.URL)
		switch {
		case errors.Is(err, service.ErrInvalidYouTubeURL):
			httpx.Error(w, http.StatusBadRequest, "not a valid youtube video url")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusAccepted, toVideoJSON(video))
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

// deleteVideoHandler removes a video record and its media object. It is
// registered admin-gated, so a non-admin never reaches it. An unknown id is
// 404; success is 204 with no body, mirroring the document delete handler.
func deleteVideoHandler(svc VideoService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := svc.Delete(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func toVideoJSON(v domain.Video) videoJSON {
	return videoJSON{
		ID:             v.ID,
		Title:          v.Title,
		Status:         string(v.Status),
		Kind:           string(v.Kind),
		ContentType:    v.ContentType,
		SizeBytes:      v.SizeBytes,
		SourceURL:      v.SourceURL,
		DurationMS:     v.DurationMS,
		Error:          v.Error,
		AnalysisStatus: string(v.AnalysisStatus),
		AnalyzedAt:     v.AnalyzedAt,
		CreatedAt:      v.CreatedAt,
		UpdatedAt:      v.UpdatedAt,
	}
}

func toPresignedJSON(p domain.PresignedRequest) presignedJSON {
	headers := p.SignedHeaders
	if headers == nil {
		headers = map[string][]string{}
	}
	return presignedJSON{URL: p.URL, Method: p.Method, Headers: headers}
}
