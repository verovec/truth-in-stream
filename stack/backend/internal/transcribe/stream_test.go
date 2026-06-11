package transcribe

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeStreamClient replays a fixed sequence of transcript events, ignoring the
// audio channel, and records the options it was called with.
type fakeStreamClient struct {
	events  []TranscriptEvent
	err     error
	gotOpts Options
}

func (f *fakeStreamClient) TranscribeStream(_ context.Context, _ <-chan []byte, opts Options) (<-chan TranscriptEvent, error) {
	f.gotOpts = opts
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan TranscriptEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestStreamSegmenterKeepsFinalizedSegmentsOnly(t *testing.T) {
	t.Parallel()

	client := &fakeStreamClient{events: []TranscriptEvent{
		{Segment: Segment{Text: "the ear"}, Final: false},
		{Segment: Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round"}, Final: true},
		{Segment: Segment{Text: "and the"}, Final: false},
		{Segment: Segment{Start: 3 * time.Second, End: 4 * time.Second, Text: "and the sky is blue"}, Final: true},
	}}
	seg := NewStreamSegmenter(client, Options{Language: "en"})

	out, err := seg.StreamSegments(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("StreamSegments: %v", err)
	}

	var got []domain.Segment
	for s := range out {
		got = append(got, s)
	}
	want := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the earth is round"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "and the sky is blue"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("segments mismatch (-want +got):\n%s", diff)
	}
	if client.gotOpts.Language != "en" {
		t.Errorf("options not forwarded: got language %q", client.gotOpts.Language)
	}
}

func TestStreamSegmenterSurfacesSetupError(t *testing.T) {
	t.Parallel()

	client := &fakeStreamClient{err: context.Canceled}
	seg := NewStreamSegmenter(client, Options{})

	out, err := seg.StreamSegments(t.Context(), make(chan []byte))
	if err == nil {
		t.Fatal("want setup error, got nil")
	}
	if out != nil {
		t.Fatalf("out = %v, want nil", out)
	}
}
