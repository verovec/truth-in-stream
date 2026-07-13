package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func sampleTVChannel(slug string) domain.TVChannel {
	return domain.TVChannel{
		Slug:           slug,
		Name:           "Channel " + slug,
		SourceKind:     domain.TVSourceYouTube,
		SourceRef:      "https://www.youtube.com/@" + slug + "/live",
		Enabled:        false,
		ArchiveEnabled: true,
	}
}

func TestStoreTVChannelCRUD(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateTVChannel(ctx, sampleTVChannel("franceinfo"))
	if err != nil {
		t.Fatalf("CreateTVChannel: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("created channel has no id")
	}
	if created.Enabled || !created.ArchiveEnabled {
		t.Fatalf("created toggles = enabled:%v archive:%v, want false/true", created.Enabled, created.ArchiveEnabled)
	}

	got, err := store.GetTVChannel(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTVChannel: %v", err)
	}
	if got.Slug != "franceinfo" || got.SourceKind != domain.TVSourceYouTube {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Update flips the toggles and edits the name; slug is immutable.
	created.Name = "franceinfo (renamed)"
	created.Enabled = true
	created.ArchiveEnabled = false
	updated, err := store.UpdateTVChannel(ctx, created)
	if err != nil {
		t.Fatalf("UpdateTVChannel: %v", err)
	}
	if updated.Name != "franceinfo (renamed)" || !updated.Enabled || updated.ArchiveEnabled {
		t.Fatalf("update did not apply: %+v", updated)
	}

	if err := store.DeleteTVChannel(ctx, created.ID); err != nil {
		t.Fatalf("DeleteTVChannel: %v", err)
	}
	if _, err := store.GetTVChannel(ctx, created.ID); !errors.Is(err, domain.ErrTVChannelNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrTVChannelNotFound", err)
	}
}

func TestStoreTVChannelDuplicateSlug(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if _, err := store.CreateTVChannel(ctx, sampleTVChannel("bfmtv")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := store.CreateTVChannel(ctx, sampleTVChannel("bfmtv"))
	if !errors.Is(err, domain.ErrDuplicateTVChannelSlug) {
		t.Fatalf("duplicate slug err = %v, want ErrDuplicateTVChannelSlug", err)
	}
}

func TestStoreTVChannelNotFound(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if _, err := store.GetTVChannel(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrTVChannelNotFound) {
		t.Fatalf("Get bad id err = %v, want ErrTVChannelNotFound", err)
	}
	if err := store.DeleteTVChannel(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, domain.ErrTVChannelNotFound) {
		t.Fatalf("Delete missing err = %v, want ErrTVChannelNotFound", err)
	}
	if _, err := store.UpdateTVChannel(ctx, domain.TVChannel{ID: "00000000-0000-0000-0000-000000000000", SourceKind: domain.TVSourceHLS}); !errors.Is(err, domain.ErrTVChannelNotFound) {
		t.Fatalf("Update missing err = %v, want ErrTVChannelNotFound", err)
	}
}

func TestStoreTVChannelListOrdersByName(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Insert out of name order; the list must come back ordered by name.
	for _, slug := range []string{"zeta", "alpha", "mike"} {
		c := sampleTVChannel(slug)
		c.Name = slug // name == slug for a deterministic sort key
		if _, err := store.CreateTVChannel(ctx, c); err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
	}
	channels, err := store.ListTVChannels(ctx)
	if err != nil {
		t.Fatalf("ListTVChannels: %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("len = %d, want 3", len(channels))
	}
	if channels[0].Name != "alpha" || channels[1].Name != "mike" || channels[2].Name != "zeta" {
		t.Fatalf("not ordered by name: %s, %s, %s", channels[0].Name, channels[1].Name, channels[2].Name)
	}
}

func TestStoreUpsertTVChannelBySlugIsIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	first, err := store.UpsertTVChannelBySlug(ctx, sampleTVChannel("lcp"))
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Operator enables the channel and disarms archiving after the seed.
	first.Enabled = true
	first.ArchiveEnabled = false
	if _, err := store.UpdateTVChannel(ctx, first); err != nil {
		t.Fatalf("operator update: %v", err)
	}

	// A reseed refreshes the descriptive fields but must not re-arm the toggles
	// the operator changed, and must keep the same id.
	reseed := sampleTVChannel("lcp")
	reseed.Name = "LCP (refreshed)"
	second, err := store.UpsertTVChannelBySlug(ctx, reseed)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("reseed changed id: %s -> %s", first.ID, second.ID)
	}
	if second.Name != "LCP (refreshed)" {
		t.Fatalf("reseed did not refresh name: %q", second.Name)
	}
	if !second.Enabled || second.ArchiveEnabled {
		t.Fatalf("reseed clobbered operator toggles: enabled:%v archive:%v", second.Enabled, second.ArchiveEnabled)
	}
}

// TestStoreTVRecordingVideoRoundTrips proves the videos table accepts a kind
// `tv` recording carrying channel_id and recorded_at, and that deleting the
// channel keeps the recording with a nulled channel_id (ON DELETE SET NULL).
func TestStoreTVRecordingVideoRoundTrips(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	channel, err := store.CreateTVChannel(ctx, sampleTVChannel("senat"))
	if err != nil {
		t.Fatalf("CreateTVChannel: %v", err)
	}
	recordedAt := time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC)
	rec, err := store.CreateVideo(ctx, domain.Video{
		Title:       "senat - 2026-07-10 20:00",
		ObjectKey:   "recordings/senat/2026/07/10/200000.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1234,
		Status:      domain.VideoStatusReady,
		Kind:        domain.VideoKindTV,
		ChannelID:   channel.ID,
		RecordedAt:  recordedAt,
	})
	if err != nil {
		t.Fatalf("CreateVideo(tv): %v", err)
	}
	if rec.Kind != domain.VideoKindTV || rec.ChannelID != channel.ID {
		t.Fatalf("tv recording mismatch: kind=%q channel=%q", rec.Kind, rec.ChannelID)
	}
	if !rec.RecordedAt.Equal(recordedAt) {
		t.Fatalf("recorded_at = %v, want %v", rec.RecordedAt.UTC(), recordedAt)
	}

	// Deleting the channel keeps the recording; its channel_id nulls out.
	if err := store.DeleteTVChannel(ctx, channel.ID); err != nil {
		t.Fatalf("DeleteTVChannel: %v", err)
	}
	got, err := store.GetVideo(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetVideo after channel delete: %v", err)
	}
	if got.ChannelID != "" {
		t.Fatalf("channel_id = %q, want empty after channel delete", got.ChannelID)
	}
	if got.Kind != domain.VideoKindTV {
		t.Fatalf("recording lost its kind: %q", got.Kind)
	}
}

func TestStoreListTVRecordingsByChannel(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	channel, err := store.CreateTVChannel(ctx, sampleTVChannel("senat"))
	if err != nil {
		t.Fatalf("CreateTVChannel: %v", err)
	}
	other, err := store.CreateTVChannel(ctx, sampleTVChannel("assemblee"))
	if err != nil {
		t.Fatalf("CreateTVChannel(other): %v", err)
	}

	base := time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC)
	mk := func(slug string, channelID string, at time.Time, status domain.VideoStatus) domain.Video {
		v, err := store.CreateVideo(ctx, domain.Video{
			Title:       "rec",
			ObjectKey:   "recordings/" + slug + "/" + at.Format("150405") + ".mp4",
			ContentType: "video/mp4",
			SizeBytes:   1,
			Status:      status,
			Kind:        domain.VideoKindTV,
			ChannelID:   channelID,
			RecordedAt:  at,
		})
		if err != nil {
			t.Fatalf("CreateVideo: %v", err)
		}
		return v
	}
	older := mk("senat", channel.ID, base, domain.VideoStatusReady)
	newer := mk("senat", channel.ID, base.Add(time.Hour), domain.VideoStatusReady)
	mk("senat", channel.ID, base.Add(2*time.Hour), domain.VideoStatusPending) // excluded: not ready
	mk("assemblee", other.ID, base, domain.VideoStatusReady)                  // excluded: other channel
	// An ordinary upload must never surface here.
	if _, err := store.CreateVideo(ctx, domain.Video{Title: "u", ObjectKey: "uploads/u.mp4", ContentType: "video/mp4", SizeBytes: 1, Status: domain.VideoStatusReady, Kind: domain.VideoKindUpload}); err != nil {
		t.Fatalf("CreateVideo(upload): %v", err)
	}

	got, err := store.ListTVRecordingsByChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("ListTVRecordingsByChannel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("recordings = %d, want 2 (only ready, only this channel)", len(got))
	}
	if got[0].ID != newer.ID || got[1].ID != older.ID {
		t.Fatalf("order = %q,%q, want newest first (%q,%q)", got[0].ID, got[1].ID, newer.ID, older.ID)
	}

	// A malformed channel id names no channel: empty list, no error.
	empty, err := store.ListTVRecordingsByChannel(ctx, "not-a-uuid")
	if err != nil {
		t.Fatalf("ListTVRecordingsByChannel(bad id): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("bad id recordings = %d, want 0", len(empty))
	}
}

// TestStoreCreateVideoWithoutChannelIsNull guards the non-tv path: an ordinary
// video has a NULL channel_id and zero recorded_at, so the new columns never
// leak onto uploads.
func TestStoreCreateVideoWithoutChannelIsNull(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	v, err := store.CreateVideo(ctx, domain.Video{
		Title:       "an upload",
		ObjectKey:   "uploads/x.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1,
		Status:      domain.VideoStatusReady,
		Kind:        domain.VideoKindUpload,
	})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	if v.ChannelID != "" {
		t.Fatalf("channel_id = %q, want empty", v.ChannelID)
	}
	if !v.RecordedAt.IsZero() {
		t.Fatalf("recorded_at = %v, want zero", v.RecordedAt)
	}
}
