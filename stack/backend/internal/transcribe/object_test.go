package transcribe

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeObjectStore serves canned bytes for a key and records the requested key.
type fakeObjectStore struct {
	body   string
	err    error
	gotKey string
}

func (f *fakeObjectStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	f.gotKey = key
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func TestObjectTranscriberDownloadsAndMapsSegments(t *testing.T) {
	t.Parallel()
	store := &fakeObjectStore{body: "OBJECT-AUDIO"}
	fake := &fakeFileTranscriber{
		transcript: Transcript{
			Segments: []Segment{
				{Start: time.Second, End: 3 * time.Second, Text: "a stored claim"},
			},
		},
	}
	ot := NewObjectTranscriber(fake, store)

	got, err := ot.Transcribe(t.Context(), "youtube/dQw4w9WgXcQ.mp4")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if store.gotKey != "youtube/dQw4w9WgXcQ.mp4" {
		t.Errorf("downloaded key = %q", store.gotKey)
	}
	if string(fake.gotAudio) != "OBJECT-AUDIO" {
		t.Errorf("streamed audio = %q, want OBJECT-AUDIO", fake.gotAudio)
	}
	// The filename passed to the provider is the object's base name.
	if fake.gotOpts.Filename != "dQw4w9WgXcQ.mp4" {
		t.Errorf("filename = %q, want base name", fake.gotOpts.Filename)
	}
	want := []domain.Segment{{Start: time.Second, End: 3 * time.Second, Text: "a stored claim"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("segments = %+v, want %+v", got, want)
	}
}

func TestObjectTranscriberDownloadError(t *testing.T) {
	t.Parallel()
	store := &fakeObjectStore{err: errors.New("no such object")}
	ot := NewObjectTranscriber(&fakeFileTranscriber{}, store)

	if _, err := ot.Transcribe(t.Context(), "youtube/missing.mp4"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// recordingResolver reports which resolver the router selected.
type recordingResolver struct {
	name      string
	gotSource string
}

func (r *recordingResolver) Transcribe(_ context.Context, source string) ([]domain.Segment, error) {
	r.gotSource = source
	return []domain.Segment{{Text: r.name}}, nil
}

func TestRouterDispatchesBySourceShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		source      string
		wantviaName string
	}{
		{name: "bare filename to local", source: "clip.mp4", wantviaName: "local"},
		{name: "object key to storage", source: "youtube/abc.mp4", wantviaName: "object"},
		{name: "uploads object key to storage", source: "uploads/uuid.mp4", wantviaName: "object"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			local := &recordingResolver{name: "local"}
			object := &recordingResolver{name: "object"}
			router := NewRouter(local, object)

			got, err := router.Transcribe(t.Context(), tc.source)
			if err != nil {
				t.Fatalf("Transcribe: %v", err)
			}
			if len(got) != 1 || got[0].Text != tc.wantviaName {
				t.Fatalf("routed via %+v, want %q", got, tc.wantviaName)
			}
		})
	}
}
