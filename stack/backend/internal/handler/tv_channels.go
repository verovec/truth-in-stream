package handler

// TV channel registry API.
//
// A channel names a free, non-DRM live source (an official YouTube live or a
// parliamentary HLS manifest) the capture worker resolves. The list drives the
// /tv page and the worker's reconcile loop; the mutations are the operator's
// single control surface. Reads serve any authenticated user; create, edit,
// and delete are admin-only, gated by middleware.RequireAdmin at registration,
// mirroring video and document ingestion.
//
//	GET    /api/tv/channels        list channels with live status
//	POST   /api/tv/channels        create a channel (admin)
//	PATCH  /api/tv/channels/{id}    edit / toggle enabled, archive_enabled (admin)
//	DELETE /api/tv/channels/{id}    remove a channel; recordings survive (admin)

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// TVChannelService is the slice of the TV channel service the registry
// endpoints consume, satisfied by *service.TVChannelService.
type TVChannelService interface {
	List(ctx context.Context) ([]domain.TVChannel, error)
	Create(ctx context.Context, in service.TVChannelInput) (domain.TVChannel, error)
	Update(ctx context.Context, id string, patch service.TVChannelPatch) (domain.TVChannel, error)
	Delete(ctx context.Context, id string) error
}

// maxTVChannelBodyBytes bounds a channel mutation body; the JSON is tiny.
const maxTVChannelBodyBytes = 1 << 20

// tvChannelJSON is the wire form of one domain.TVChannel plus the computed live
// flag. Live is hardcoded false until the live hub card (VER-211) enriches the
// list from connected publisher feeds; the field ships now so consumers never
// change shape when it goes live.
type tvChannelJSON struct {
	ID             string    `json:"id"`
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	SourceKind     string    `json:"source_kind"`
	SourceRef      string    `json:"source_ref"`
	Enabled        bool      `json:"enabled"`
	ArchiveEnabled bool      `json:"archive_enabled"`
	Live           bool      `json:"live"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type listTVChannelsResponse struct {
	Channels []tvChannelJSON `json:"channels"`
}

type createTVChannelBody struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	SourceKind string `json:"source_kind"`
	SourceRef  string `json:"source_ref"`
	Enabled    bool   `json:"enabled"`
	// ArchiveEnabled is a pointer so an omitted field defaults to true (the safe
	// posture: a captured channel archives unless the operator opts out), while
	// an explicit false is honored.
	ArchiveEnabled *bool `json:"archive_enabled"`
}

// patchTVChannelBody uses pointers so an omitted field is left unchanged and a
// present field (including a false bool) is applied.
type patchTVChannelBody struct {
	Name           *string `json:"name"`
	SourceKind     *string `json:"source_kind"`
	SourceRef      *string `json:"source_ref"`
	Enabled        *bool   `json:"enabled"`
	ArchiveEnabled *bool   `json:"archive_enabled"`
}

func listTVChannelsHandler(svc TVChannelService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channels, err := svc.List(r.Context())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		out := make([]tvChannelJSON, 0, len(channels))
		for _, c := range channels {
			out = append(out, toTVChannelJSON(c))
		}
		httpx.JSON(w, http.StatusOK, listTVChannelsResponse{Channels: out})
	}
}

func createTVChannelHandler(svc TVChannelService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body createTVChannelBody
		if !decodeJSONBody(w, r, maxTVChannelBodyBytes, &body) {
			return
		}
		archiveEnabled := true
		if body.ArchiveEnabled != nil {
			archiveEnabled = *body.ArchiveEnabled
		}
		channel, err := svc.Create(r.Context(), service.TVChannelInput{
			Slug:           body.Slug,
			Name:           body.Name,
			SourceKind:     domain.TVSourceKind(body.SourceKind),
			SourceRef:      body.SourceRef,
			Enabled:        body.Enabled,
			ArchiveEnabled: archiveEnabled,
		})
		switch {
		case errors.Is(err, service.ErrTVChannelInvalidSlug):
			httpx.Error(w, http.StatusBadRequest, "slug must be a lowercase, dash-separated token")
		case errors.Is(err, service.ErrTVChannelInvalidName):
			httpx.Error(w, http.StatusBadRequest, "name is required")
		case errors.Is(err, service.ErrTVChannelInvalidKind):
			httpx.Error(w, http.StatusBadRequest, "source_kind must be youtube or hls")
		case errors.Is(err, service.ErrTVChannelInvalidSource):
			httpx.Error(w, http.StatusBadRequest, "source_ref is required")
		case errors.Is(err, domain.ErrDuplicateTVChannelSlug):
			httpx.Error(w, http.StatusConflict, "a channel with that slug already exists")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusCreated, toTVChannelJSON(channel))
		}
	}
}

func updateTVChannelHandler(svc TVChannelService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body patchTVChannelBody
		if !decodeJSONBody(w, r, maxTVChannelBodyBytes, &body) {
			return
		}
		patch := service.TVChannelPatch{
			Name:           body.Name,
			SourceRef:      body.SourceRef,
			Enabled:        body.Enabled,
			ArchiveEnabled: body.ArchiveEnabled,
		}
		if body.SourceKind != nil {
			kind := domain.TVSourceKind(*body.SourceKind)
			patch.SourceKind = &kind
		}
		channel, err := svc.Update(r.Context(), r.PathValue("id"), patch)
		switch {
		case errors.Is(err, domain.ErrTVChannelNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown channel")
		case errors.Is(err, service.ErrTVChannelInvalidName):
			httpx.Error(w, http.StatusBadRequest, "name is required")
		case errors.Is(err, service.ErrTVChannelInvalidKind):
			httpx.Error(w, http.StatusBadRequest, "source_kind must be youtube or hls")
		case errors.Is(err, service.ErrTVChannelInvalidSource):
			httpx.Error(w, http.StatusBadRequest, "source_ref is required")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			httpx.JSON(w, http.StatusOK, toTVChannelJSON(channel))
		}
	}
}

// deleteTVChannelHandler removes a channel. It is registered admin-gated, so a
// non-admin never reaches it. An unknown id is 404; success is 204 with no body,
// mirroring the video and document delete handlers. Recordings survive the
// delete (the videos.channel_id FK nulls out).
func deleteTVChannelHandler(svc TVChannelService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := svc.Delete(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, domain.ErrTVChannelNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown channel")
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func toTVChannelJSON(c domain.TVChannel) tvChannelJSON {
	return tvChannelJSON{
		ID:             c.ID,
		Slug:           c.Slug,
		Name:           c.Name,
		SourceKind:     string(c.SourceKind),
		SourceRef:      c.SourceRef,
		Enabled:        c.Enabled,
		ArchiveEnabled: c.ArchiveEnabled,
		Live:           false,
		CreatedAt:      c.CreatedAt,
		UpdatedAt:      c.UpdatedAt,
	}
}
