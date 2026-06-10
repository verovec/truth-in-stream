package transcribe

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeFileTranscriber struct {
	transcript Transcript
	err        error
	gotAudio   []byte
	gotOpts    Options
	called     bool
}

func (f *fakeFileTranscriber) TranscribeFile(_ context.Context, audio io.Reader, opts Options) (Transcript, error) {
	f.called = true
	b, _ := io.ReadAll(audio)
	f.gotAudio = b
	f.gotOpts = opts
	return f.transcript, f.err
}

func writeMedia(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
}

func TestSourceTranscriberReadsMediaAndMapsSegments(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeMedia(t, root, "clip.m4a", "AUDIO-BYTES")
	fake := &fakeFileTranscriber{
		transcript: Transcript{
			Language: "en",
			Segments: []Segment{
				{Start: 1500 * time.Millisecond, End: 4200 * time.Millisecond, Text: "the great wall is visible from space"},
				{Start: 5 * time.Second, End: 7 * time.Second, Text: "we only use ten percent of our brains"},
			},
		},
	}
	st := NewSourceTranscriber(fake, root)

	got, err := st.Transcribe(context.Background(), "clip.m4a")
	if err != nil {
		t.Fatalf("Transcribe returned error: %v", err)
	}

	want := []domain.Segment{
		{Start: 1500 * time.Millisecond, End: 4200 * time.Millisecond, Text: "the great wall is visible from space"},
		{Start: 5 * time.Second, End: 7 * time.Second, Text: "we only use ten percent of our brains"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("segments mismatch (-want +got):\n%s", diff)
	}
	if string(fake.gotAudio) != "AUDIO-BYTES" {
		t.Errorf("transcriber read %q, want the media bytes", fake.gotAudio)
	}
	if fake.gotOpts.Filename != "clip.m4a" {
		t.Errorf("Filename = %q, want clip.m4a", fake.gotOpts.Filename)
	}
}

func TestSourceTranscriberRejectsUnsafeSource(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"../secrets.env", "sub/clip.m4a", "", ".", ".."} {
		fake := &fakeFileTranscriber{}
		st := NewSourceTranscriber(fake, t.TempDir())
		if _, err := st.Transcribe(context.Background(), source); err == nil {
			t.Errorf("source %q: expected error, got nil", source)
		}
		if fake.called {
			t.Errorf("source %q: transcriber must not be called for an unsafe source", source)
		}
	}
}

func TestSourceTranscriberMissingFile(t *testing.T) {
	t.Parallel()

	st := NewSourceTranscriber(&fakeFileTranscriber{}, t.TempDir())
	if _, err := st.Transcribe(context.Background(), "absent.m4a"); err == nil {
		t.Fatal("expected error for a missing media file, got nil")
	}
}

func TestSourceTranscriberPropagatesTranscriberError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeMedia(t, root, "clip.m4a", "x")
	wantErr := errors.New("scribe down")
	st := NewSourceTranscriber(&fakeFileTranscriber{err: wantErr}, root)

	if _, err := st.Transcribe(context.Background(), "clip.m4a"); !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped %v, got %v", wantErr, err)
	}
}
