package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TV channel service errors. They classify a bad request so the handler maps
// each to its own status code; ErrTVChannelNotFound and ErrDuplicateTVChannelSlug
// live in the domain package because the store raises them.
var (
	// ErrTVChannelInvalidSlug is returned when a create request carries a slug
	// that is not a lowercase, dash-separated token.
	ErrTVChannelInvalidSlug = errors.New("tv channel: slug is invalid")
	// ErrTVChannelInvalidName is returned when a channel's name is blank.
	ErrTVChannelInvalidName = errors.New("tv channel: name is required")
	// ErrTVChannelInvalidKind is returned for a source kind other than youtube or hls.
	ErrTVChannelInvalidKind = errors.New("tv channel: unsupported source kind")
	// ErrTVChannelInvalidSource is returned when a channel's source ref is blank.
	ErrTVChannelInvalidSource = errors.New("tv channel: source ref is required")
)

// tvChannelSlugRE constrains a slug to a lowercase, dash-separated token so it
// is safe as a storage path segment and a stable registry key.
var tvChannelSlugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// TVChannelInput is the create payload: the channel's identity, source, and
// initial toggle state.
type TVChannelInput struct {
	Slug           string
	Name           string
	SourceKind     domain.TVSourceKind
	SourceRef      string
	Enabled        bool
	ArchiveEnabled bool
}

// TVChannelPatch carries the optional fields of a partial update. A nil field is
// left unchanged; a non-nil field (including a false bool) is applied, so the
// enable and archive toggles can each be flipped independently.
type TVChannelPatch struct {
	Name           *string
	SourceKind     *domain.TVSourceKind
	SourceRef      *string
	Enabled        *bool
	ArchiveEnabled *bool
}

// TVChannelService owns the TV channel registry: it validates and stores
// channels and lists, updates, and deletes them. It holds no HTTP types.
type TVChannelService struct {
	store domain.TVChannelStore
}

// NewTVChannelService builds a TVChannelService. It fails fast on a nil store.
func NewTVChannelService(store domain.TVChannelStore) (*TVChannelService, error) {
	if store == nil {
		return nil, errors.New("tv channel: store is required")
	}
	return &TVChannelService{store: store}, nil
}

// List returns every channel, ordered by name. It drives both the /tv page and
// the capture worker's reconcile loop.
func (s *TVChannelService) List(ctx context.Context) ([]domain.TVChannel, error) {
	channels, err := s.store.ListTVChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("tv channel: list: %w", err)
	}
	return channels, nil
}

// Create validates the input and inserts a channel, returning the stored record
// or a classification error the handler maps to a 4xx. A slug collision surfaces
// as domain.ErrDuplicateTVChannelSlug.
func (s *TVChannelService) Create(ctx context.Context, in TVChannelInput) (domain.TVChannel, error) {
	slug := strings.TrimSpace(in.Slug)
	if !tvChannelSlugRE.MatchString(slug) {
		return domain.TVChannel{}, ErrTVChannelInvalidSlug
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return domain.TVChannel{}, ErrTVChannelInvalidName
	}
	if !in.SourceKind.Valid() {
		return domain.TVChannel{}, ErrTVChannelInvalidKind
	}
	sourceRef := strings.TrimSpace(in.SourceRef)
	if sourceRef == "" {
		return domain.TVChannel{}, ErrTVChannelInvalidSource
	}
	created, err := s.store.CreateTVChannel(ctx, domain.TVChannel{
		Slug:           slug,
		Name:           name,
		SourceKind:     in.SourceKind,
		SourceRef:      sourceRef,
		Enabled:        in.Enabled,
		ArchiveEnabled: in.ArchiveEnabled,
	})
	if err != nil {
		if errors.Is(err, domain.ErrDuplicateTVChannelSlug) {
			return domain.TVChannel{}, err
		}
		return domain.TVChannel{}, fmt.Errorf("tv channel: create: %w", err)
	}
	return created, nil
}

// Update applies a partial patch to an existing channel. It reads the current
// record, overlays the provided fields (validating each), and writes the result,
// so an omitted field keeps its stored value. An unknown id surfaces as
// domain.ErrTVChannelNotFound.
func (s *TVChannelService) Update(ctx context.Context, id string, patch TVChannelPatch) (domain.TVChannel, error) {
	current, err := s.store.GetTVChannel(ctx, id)
	if err != nil {
		return domain.TVChannel{}, err
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return domain.TVChannel{}, ErrTVChannelInvalidName
		}
		current.Name = name
	}
	if patch.SourceKind != nil {
		if !patch.SourceKind.Valid() {
			return domain.TVChannel{}, ErrTVChannelInvalidKind
		}
		current.SourceKind = *patch.SourceKind
	}
	if patch.SourceRef != nil {
		sourceRef := strings.TrimSpace(*patch.SourceRef)
		if sourceRef == "" {
			return domain.TVChannel{}, ErrTVChannelInvalidSource
		}
		current.SourceRef = sourceRef
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if patch.ArchiveEnabled != nil {
		current.ArchiveEnabled = *patch.ArchiveEnabled
	}
	updated, err := s.store.UpdateTVChannel(ctx, current)
	if err != nil {
		return domain.TVChannel{}, fmt.Errorf("tv channel: update %s: %w", id, err)
	}
	return updated, nil
}

// Delete removes a channel by id. An unknown id surfaces as
// domain.ErrTVChannelNotFound (passed through from the store).
func (s *TVChannelService) Delete(ctx context.Context, id string) error {
	if err := s.store.DeleteTVChannel(ctx, id); err != nil {
		if errors.Is(err, domain.ErrTVChannelNotFound) {
			return err
		}
		return fmt.Errorf("tv channel: delete %s: %w", id, err)
	}
	return nil
}
