package service

import (
	"context"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeTVChannelStore is an in-memory TVChannelStore for service tests. It keys
// channels by id and enforces slug uniqueness so the create path's duplicate
// mapping is exercised without a database.
type fakeTVChannelStore struct {
	byID      map[string]domain.TVChannel
	nextID    int
	createErr error
	listErr   error
	updateErr error
	deleteErr error
}

func newFakeTVChannelStore() *fakeTVChannelStore {
	return &fakeTVChannelStore{byID: map[string]domain.TVChannel{}}
}

func (f *fakeTVChannelStore) CreateTVChannel(_ context.Context, c domain.TVChannel) (domain.TVChannel, error) {
	if f.createErr != nil {
		return domain.TVChannel{}, f.createErr
	}
	for _, existing := range f.byID {
		if existing.Slug == c.Slug {
			return domain.TVChannel{}, domain.ErrDuplicateTVChannelSlug
		}
	}
	f.nextID++
	c.ID = string(rune('a' + f.nextID))
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeTVChannelStore) GetTVChannel(_ context.Context, id string) (domain.TVChannel, error) {
	c, ok := f.byID[id]
	if !ok {
		return domain.TVChannel{}, domain.ErrTVChannelNotFound
	}
	return c, nil
}

func (f *fakeTVChannelStore) ListTVChannels(_ context.Context) ([]domain.TVChannel, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]domain.TVChannel, 0, len(f.byID))
	for _, c := range f.byID {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeTVChannelStore) UpdateTVChannel(_ context.Context, c domain.TVChannel) (domain.TVChannel, error) {
	if f.updateErr != nil {
		return domain.TVChannel{}, f.updateErr
	}
	if _, ok := f.byID[c.ID]; !ok {
		return domain.TVChannel{}, domain.ErrTVChannelNotFound
	}
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeTVChannelStore) DeleteTVChannel(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.byID[id]; !ok {
		return domain.ErrTVChannelNotFound
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeTVChannelStore) UpsertTVChannelBySlug(_ context.Context, c domain.TVChannel) (domain.TVChannel, error) {
	return c, nil
}

func newTVChannelService(t *testing.T, store domain.TVChannelStore) *TVChannelService {
	t.Helper()
	svc, err := NewTVChannelService(store)
	if err != nil {
		t.Fatalf("NewTVChannelService: %v", err)
	}
	return svc
}

func TestNewTVChannelServiceRequiresStore(t *testing.T) {
	t.Parallel()
	if _, err := NewTVChannelService(nil); err == nil {
		t.Fatal("NewTVChannelService(nil) should error")
	}
}

func TestTVChannelServiceCreateValidation(t *testing.T) {
	t.Parallel()
	valid := TVChannelInput{
		Slug:       "franceinfo",
		Name:       "franceinfo",
		SourceKind: domain.TVSourceYouTube,
		SourceRef:  "https://www.youtube.com/franceinfo/live",
	}
	tests := []struct {
		name    string
		mutate  func(in *TVChannelInput)
		wantErr error
	}{
		{name: "valid", mutate: func(*TVChannelInput) {}, wantErr: nil},
		{name: "blank slug", mutate: func(in *TVChannelInput) { in.Slug = "  " }, wantErr: ErrTVChannelInvalidSlug},
		{name: "uppercase slug", mutate: func(in *TVChannelInput) { in.Slug = "FranceInfo" }, wantErr: ErrTVChannelInvalidSlug},
		{name: "slug with space", mutate: func(in *TVChannelInput) { in.Slug = "france info" }, wantErr: ErrTVChannelInvalidSlug},
		{name: "blank name", mutate: func(in *TVChannelInput) { in.Name = " " }, wantErr: ErrTVChannelInvalidName},
		{name: "bad kind", mutate: func(in *TVChannelInput) { in.SourceKind = "widevine" }, wantErr: ErrTVChannelInvalidKind},
		{name: "blank source", mutate: func(in *TVChannelInput) { in.SourceRef = "" }, wantErr: ErrTVChannelInvalidSource},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newTVChannelService(t, newFakeTVChannelStore())
			in := valid
			tc.mutate(&in)
			_, err := svc.Create(t.Context(), in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Create err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestTVChannelServiceCreateStoresTrimmedDefaults(t *testing.T) {
	t.Parallel()
	svc := newTVChannelService(t, newFakeTVChannelStore())
	got, err := svc.Create(t.Context(), TVChannelInput{
		Slug:           " bfmtv ",
		Name:           "  BFMTV  ",
		SourceKind:     domain.TVSourceYouTube,
		SourceRef:      "  https://www.youtube.com/@BFMTV/live  ",
		Enabled:        false,
		ArchiveEnabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Slug != "bfmtv" || got.Name != "BFMTV" || got.SourceRef != "https://www.youtube.com/@BFMTV/live" {
		t.Fatalf("Create did not trim fields: %+v", got)
	}
	if got.Enabled {
		t.Fatalf("Create should honor Enabled=false")
	}
	if !got.ArchiveEnabled {
		t.Fatalf("Create should honor ArchiveEnabled=true")
	}
}

func TestTVChannelServiceCreateDuplicateSlug(t *testing.T) {
	t.Parallel()
	svc := newTVChannelService(t, newFakeTVChannelStore())
	in := TVChannelInput{Slug: "lci", Name: "LCI", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@LCI/live"}
	if _, err := svc.Create(t.Context(), in); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := svc.Create(t.Context(), in)
	if !errors.Is(err, domain.ErrDuplicateTVChannelSlug) {
		t.Fatalf("second Create err = %v, want ErrDuplicateTVChannelSlug", err)
	}
}

func TestTVChannelServiceUpdatePartial(t *testing.T) {
	t.Parallel()
	store := newFakeTVChannelStore()
	svc := newTVChannelService(t, store)
	created, err := svc.Create(t.Context(), TVChannelInput{
		Slug: "cnews", Name: "CNEWS", SourceKind: domain.TVSourceYouTube,
		SourceRef: "https://www.youtube.com/@CNEWSofficiel/live", Enabled: false, ArchiveEnabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	enabled := true
	archive := false
	updated, err := svc.Update(t.Context(), created.ID, TVChannelPatch{Enabled: &enabled, ArchiveEnabled: &archive})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !updated.Enabled {
		t.Fatalf("Update should enable the channel")
	}
	if updated.ArchiveEnabled {
		t.Fatalf("Update should disable archiving")
	}
	// Untouched fields keep their stored value.
	if updated.Name != "CNEWS" || updated.SourceRef != created.SourceRef || updated.Slug != "cnews" {
		t.Fatalf("Update mutated an omitted field: %+v", updated)
	}
}

func TestTVChannelServiceUpdateValidationAndNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeTVChannelStore()
	svc := newTVChannelService(t, store)
	created, err := svc.Create(t.Context(), TVChannelInput{
		Slug: "lcp", Name: "LCP", SourceKind: domain.TVSourceYouTube, SourceRef: "https://www.youtube.com/@LCP/live",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	blank := "   "
	if _, err := svc.Update(t.Context(), created.ID, TVChannelPatch{Name: &blank}); !errors.Is(err, ErrTVChannelInvalidName) {
		t.Fatalf("Update blank name err = %v, want ErrTVChannelInvalidName", err)
	}
	badKind := domain.TVSourceKind("widevine")
	if _, err := svc.Update(t.Context(), created.ID, TVChannelPatch{SourceKind: &badKind}); !errors.Is(err, ErrTVChannelInvalidKind) {
		t.Fatalf("Update bad kind err = %v, want ErrTVChannelInvalidKind", err)
	}
	if _, err := svc.Update(t.Context(), "missing", TVChannelPatch{Enabled: &[]bool{true}[0]}); !errors.Is(err, domain.ErrTVChannelNotFound) {
		t.Fatalf("Update missing id err = %v, want ErrTVChannelNotFound", err)
	}
}

func TestTVChannelServiceDelete(t *testing.T) {
	t.Parallel()
	store := newFakeTVChannelStore()
	svc := newTVChannelService(t, store)
	created, err := svc.Create(t.Context(), TVChannelInput{
		Slug: "senat", Name: "Senat", SourceKind: domain.TVSourceHLS, SourceRef: "https://videos.senat.fr/direct",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(t.Context(), created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(t.Context(), created.ID); !errors.Is(err, domain.ErrTVChannelNotFound) {
		t.Fatalf("second Delete err = %v, want ErrTVChannelNotFound", err)
	}
}
