package audioextract

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeClock records requested sleeps and advances its own now by each, so
// pacing is asserted without any real delay. Reads of sleeps are synchronized
// by the frame channel close, which happens after the pump goroutine's last
// Sleep call.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

// writeScript installs a fake ffmpeg as a shell script so exec plumbing is
// tested without the real binary.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

// startExtract starts an extraction, retrying the brief ETXTBSY window Linux
// leaves when a concurrently forking parallel test still holds the freshly
// written fake-ffmpeg script's descriptor at exec time (Go issue 22315). Only
// the start is retried; a test asserting a start failure calls Extract
// directly.
func startExtract(ctx context.Context, t *testing.T, e *Extractor, src Source) *Stream {
	t.Helper()
	for attempt := 0; ; attempt++ {
		s, err := e.Extract(ctx, src)
		if err == nil {
			return s
		}
		if attempt < 50 && errors.Is(err, syscall.ETXTBSY) {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		t.Fatalf("Extract: %v", err)
		return nil
	}
}

func newTestExtractor(t *testing.T, binary string, factor float64) (*Extractor, *fakeClock) {
	t.Helper()
	e, err := New(Config{BinaryPath: binary, PacingFactor: factor})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clk := &fakeClock{now: time.Unix(0, 0)}
	e.clk = clk
	return e, clk
}

// collectFrames drains the stream to closure under a watchdog so a regression
// can never hang the suite.
func collectFrames(t *testing.T, s *Stream) [][]byte {
	t.Helper()
	var frames [][]byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		for f := range s.Frames() {
			frames = append(frames, f)
		}
	}()
	select {
	case <-done:
		return frames
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for frame stream to close")
		return nil
	}
}

func TestNewRejectsInvalidPacingFactor(t *testing.T) {
	t.Parallel()
	for _, factor := range []float64{-1, -0.001, 1e-9, 1e9, math.NaN(), math.Inf(1)} {
		if _, err := New(Config{PacingFactor: factor}); err == nil {
			t.Errorf("New accepted pacing factor %v", factor)
		}
	}
	e, err := New(Config{})
	if err != nil {
		t.Fatalf("New with defaults: %v", err)
	}
	if e.factor != DefaultPacingFactor {
		t.Errorf("default factor = %v, want %v", e.factor, DefaultPacingFactor)
	}
}

func TestExtractHappyPathFramesAndPacing(t *testing.T) {
	t.Parallel()
	// 8000 bytes = 250 ms: two full 100 ms frames plus an exactly-minimum
	// 50 ms tail that must be flushed, not dropped.
	script := writeScript(t, "head -c 8000 /dev/zero")
	e, clk := newTestExtractor(t, script, 1.0)

	s := startExtract(t.Context(), t, e, Source{Input: "in.mp4"})
	frames := collectFrames(t, s)
	if s.Err() != nil {
		t.Fatalf("Err = %v, want nil", s.Err())
	}

	wantLens := []int{3200, 3200, 1600}
	if len(frames) != len(wantLens) {
		t.Fatalf("got %d frames, want %d", len(frames), len(wantLens))
	}
	for i, f := range frames {
		if len(f) != wantLens[i] {
			t.Errorf("frame %d len = %d, want %d", i, len(f), wantLens[i])
		}
		if len(f) < MinFrameMillis*BytesPerMilli || len(f) > FrameMillis*BytesPerMilli {
			t.Errorf("frame %d len %d outside AssemblyAI bounds", i, len(f))
		}
	}

	// Frame one goes out immediately; each later frame waits for the wall
	// clock to reach the audio already sent.
	wantSleeps := []time.Duration{100 * time.Millisecond, 100 * time.Millisecond}
	if len(clk.sleeps) != len(wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", clk.sleeps, wantSleeps)
	}
	for i, d := range clk.sleeps {
		if d != wantSleeps[i] {
			t.Errorf("sleep %d = %v, want %v", i, d, wantSleeps[i])
		}
	}
}

func TestExtractSlowerFactorStretchesPacing(t *testing.T) {
	t.Parallel()
	script := writeScript(t, "head -c 9600 /dev/zero")
	e, clk := newTestExtractor(t, script, 0.5)

	s := startExtract(t.Context(), t, e, Source{Input: "in.mp4"})
	if got := len(collectFrames(t, s)); got != 3 {
		t.Fatalf("got %d frames, want 3", got)
	}
	wantSleeps := []time.Duration{200 * time.Millisecond, 200 * time.Millisecond}
	if len(clk.sleeps) != len(wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", clk.sleeps, wantSleeps)
	}
	for i, d := range clk.sleeps {
		if d != wantSleeps[i] {
			t.Errorf("sleep %d = %v, want %v", i, d, wantSleeps[i])
		}
	}
}

func TestExtractDropsSubMinimumTail(t *testing.T) {
	t.Parallel()
	// 4000 bytes: one full frame plus a 25 ms tail, below AssemblyAI's 50 ms
	// floor, which must be dropped rather than sent.
	script := writeScript(t, "head -c 4000 /dev/zero")
	e, _ := newTestExtractor(t, script, 1.0)

	s := startExtract(t.Context(), t, e, Source{Input: "in.mp4"})
	frames := collectFrames(t, s)
	if s.Err() != nil {
		t.Fatalf("Err = %v, want nil", s.Err())
	}
	if len(frames) != 1 || len(frames[0]) != 3200 {
		t.Fatalf("frames = %d, want exactly one full frame", len(frames))
	}
}

func TestExtractTrimsOddTrailingByte(t *testing.T) {
	t.Parallel()
	// 1601 bytes: a malformed stream that ends mid-sample. The odd byte is
	// trimmed so the emitted tail stays sample-aligned at exactly 50 ms.
	script := writeScript(t, "head -c 1601 /dev/zero")
	e, _ := newTestExtractor(t, script, 1.0)

	s := startExtract(t.Context(), t, e, Source{Input: "in.mp4"})
	frames := collectFrames(t, s)
	if s.Err() != nil {
		t.Fatalf("Err = %v, want nil", s.Err())
	}
	if len(frames) != 1 || len(frames[0]) != 1600 {
		t.Fatalf("got %d frames, want one 1600-byte frame", len(frames))
	}
}

func TestExtractEmptyOutputCompletesWithNoFrames(t *testing.T) {
	t.Parallel()
	script := writeScript(t, "exit 0")
	e, _ := newTestExtractor(t, script, 1.0)

	s := startExtract(t.Context(), t, e, Source{Input: "in.mp4"})
	if frames := collectFrames(t, s); len(frames) != 0 {
		t.Fatalf("got %d frames, want none", len(frames))
	}
	if s.Err() != nil {
		t.Fatalf("Err = %v, want nil", s.Err())
	}
}

func TestExtractFFmpegFailureSurfacesTypedError(t *testing.T) {
	t.Parallel()
	script := writeScript(t, `echo "in.mp4: Invalid data found when processing input" >&2; exit 1`)
	e, _ := newTestExtractor(t, script, 1.0)

	s := startExtract(t.Context(), t, e, Source{Input: "in.mp4"})
	if frames := collectFrames(t, s); len(frames) != 0 {
		t.Fatalf("got %d frames from a failed run", len(frames))
	}

	var ffErr *FFmpegError
	if !errors.As(s.Err(), &ffErr) {
		t.Fatalf("Err = %v (%T), want *FFmpegError", s.Err(), s.Err())
	}
	if !strings.Contains(ffErr.Stderr, "Invalid data found") {
		t.Errorf("Stderr = %q, want the ffmpeg diagnostic", ffErr.Stderr)
	}
	var exitErr *exec.ExitError
	if !errors.As(s.Err(), &exitErr) {
		t.Errorf("Err does not unwrap to the exit error: %v", s.Err())
	}
}

func TestExtractFailureMidStreamDeliversFramesThenError(t *testing.T) {
	t.Parallel()
	script := writeScript(t, `head -c 3200 /dev/zero; echo "demux error" >&2; exit 1`)
	e, _ := newTestExtractor(t, script, 1.0)

	s := startExtract(t.Context(), t, e, Source{Input: "in.mp4"})
	frames := collectFrames(t, s)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want the one emitted before the failure", len(frames))
	}
	var ffErr *FFmpegError
	if !errors.As(s.Err(), &ffErr) {
		t.Fatalf("Err = %v, want *FFmpegError", s.Err())
	}
	if !strings.Contains(ffErr.Stderr, "demux error") {
		t.Errorf("Stderr = %q, want the diagnostic", ffErr.Stderr)
	}
}

func TestExtractMissingBinaryFailsFast(t *testing.T) {
	t.Parallel()
	e, _ := newTestExtractor(t, filepath.Join(t.TempDir(), "absent-ffmpeg"), 1.0)

	s, err := e.Extract(t.Context(), Source{Input: "in.mp4"})
	if err == nil {
		t.Fatal("Extract succeeded with a missing binary")
	}
	if s != nil {
		t.Error("Extract returned a stream alongside the error")
	}
	var ffErr *FFmpegError
	if !errors.As(err, &ffErr) {
		t.Fatalf("err = %v (%T), want *FFmpegError", err, err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist in the chain", err)
	}
}

func TestExtractEmptySourceRejected(t *testing.T) {
	t.Parallel()
	e, _ := newTestExtractor(t, "ffmpeg", 1.0)
	if _, err := e.Extract(t.Context(), Source{}); err == nil {
		t.Fatal("Extract accepted an empty source")
	}
}

func TestExtractContextCancelStopsStream(t *testing.T) {
	t.Parallel()
	// exec replaces the shell so the kill on cancel reaches the process
	// holding the pipes, guaranteeing prompt teardown.
	script := writeScript(t, "head -c 3200 /dev/zero; exec sleep 60")
	e, _ := newTestExtractor(t, script, 1.0)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := startExtract(ctx, t, e, Source{Input: "in.mp4"})

	select {
	case _, ok := <-s.Frames():
		if !ok {
			t.Fatal("stream closed before the first frame")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the first frame")
	}

	cancel()
	if frames := collectFrames(t, s); len(frames) != 0 {
		t.Fatalf("got %d frames after cancel", len(frames))
	}
	if !errors.Is(s.Err(), context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", s.Err())
	}
}

func TestBuildArgs(t *testing.T) {
	t.Parallel()
	t.Run("with headers", func(t *testing.T) {
		t.Parallel()
		args := buildArgs(Source{
			Input:  "https://storage.example/videos/clip.mp4?sig=abc",
			Header: map[string][]string{"x-amz-meta-kind": {"video"}},
		})
		headersAt := indexOf(t, args, "-headers")
		inputAt := indexOf(t, args, "-i")
		if headersAt > inputAt {
			t.Error("-headers must precede -i to apply to the input")
		}
		if got := args[headersAt+1]; got != "x-amz-meta-kind: video\r\n" {
			t.Errorf("header block = %q", got)
		}
		if got := args[inputAt+1]; got != "https://storage.example/videos/clip.mp4?sig=abc" {
			t.Errorf("input = %q", got)
		}
		if got := args[len(args)-1]; got != "pipe:1" {
			t.Errorf("final arg = %q, want pipe:1", got)
		}
		for _, want := range [][2]string{{"-map", "0:a:0"}, {"-f", "s16le"}, {"-ar", "16000"}, {"-ac", "1"}} {
			at := indexOf(t, args, want[0])
			if args[at+1] != want[1] {
				t.Errorf("%s = %q, want %q", want[0], args[at+1], want[1])
			}
		}
	})
	t.Run("without headers", func(t *testing.T) {
		t.Parallel()
		args := buildArgs(Source{Input: "/data/clip.mp4"})
		for _, a := range args {
			if a == "-headers" {
				t.Fatalf("-headers present without header values: %v", args)
			}
		}
	})
}

func indexOf(t *testing.T, args []string, flag string) int {
	t.Helper()
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	t.Fatalf("flag %s not in %v", flag, args)
	return -1
}

func TestHeaderBlock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header map[string][]string
		want   string
	}{
		{name: "empty", header: nil, want: ""},
		{
			name:   "host skipped case-insensitively",
			header: map[string][]string{"Host": {"storage.example"}, "host": {"other"}},
			want:   "",
		},
		{
			name: "sorted keys and multi-values",
			header: map[string][]string{
				"x-b": {"2"},
				"x-a": {"1", "3"},
			},
			want: "x-a: 1\r\nx-a: 3\r\nx-b: 2\r\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := headerBlock(tc.header); got != tc.want {
				t.Errorf("headerBlock = %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeMediaStore is the minimal domain.MediaStore for PresignedSource tests.
type fakeMediaStore struct {
	req domain.PresignedRequest
	err error
}

func (f *fakeMediaStore) PresignUpload(context.Context, string) (domain.PresignedRequest, error) {
	return domain.PresignedRequest{}, nil
}

func (f *fakeMediaStore) PresignUploadOnce(context.Context, string, string, int64) (domain.PresignedRequest, error) {
	return domain.PresignedRequest{}, nil
}

func (f *fakeMediaStore) PresignDownload(context.Context, string) (domain.PresignedRequest, error) {
	return f.req, f.err
}

func (f *fakeMediaStore) Exists(context.Context, string) (bool, error) { return false, nil }

func (f *fakeMediaStore) Delete(context.Context, string) error { return nil }

func TestPresignedSource(t *testing.T) {
	t.Parallel()
	t.Run("maps url and headers", func(t *testing.T) {
		t.Parallel()
		store := &fakeMediaStore{req: domain.PresignedRequest{
			URL:           "https://storage.example/videos/clip.mp4?sig=abc",
			Method:        "GET",
			SignedHeaders: map[string][]string{"host": {"storage.example"}},
		}}
		src, err := PresignedSource(t.Context(), store, "videos/clip.mp4")
		if err != nil {
			t.Fatalf("PresignedSource: %v", err)
		}
		if src.Input != store.req.URL {
			t.Errorf("Input = %q, want the presigned URL", src.Input)
		}
		if len(src.Header) != 1 {
			t.Errorf("Header = %v, want the signed headers", src.Header)
		}
	})
	t.Run("wraps presign failure", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("storage down")
		store := &fakeMediaStore{err: sentinel}
		if _, err := PresignedSource(t.Context(), store, "videos/clip.mp4"); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want the presign error in the chain", err)
		}
	})
}

// TestExtractRealFFmpeg runs the real binary over a synthesized WAV to prove
// the argument vector works end to end. It skips cleanly when ffmpeg is not
// installed, so the suite never needs it.
func TestExtractRealFFmpeg(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	const samples = 8000 // 500 ms at 16 kHz
	path := filepath.Join(t.TempDir(), "in.wav")
	if err := os.WriteFile(path, wavFile(samples), 0o644); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	e, _ := newTestExtractor(t, "", 1.0)

	s := startExtract(t.Context(), t, e, Source{Input: path})
	frames := collectFrames(t, s)
	if s.Err() != nil {
		t.Fatalf("Err = %v, want nil", s.Err())
	}
	total := 0
	for i, f := range frames {
		total += len(f)
		if len(f) < MinFrameMillis*BytesPerMilli || len(f) > FrameMillis*BytesPerMilli {
			t.Errorf("frame %d len %d outside AssemblyAI bounds", i, len(f))
		}
	}
	if total != samples*2 {
		t.Errorf("total bytes = %d, want %d", total, samples*2)
	}
}

// wavFile builds a minimal 16 kHz mono s16le RIFF/WAVE file of silent samples.
func wavFile(samples int) []byte {
	data := samples * 2
	b := make([]byte, 0, 44+data)
	b = append(b, "RIFF"...)
	b = le32(b, uint32(36+data))
	b = append(b, "WAVEfmt "...)
	b = le32(b, 16)
	b = le16(b, 1) // PCM
	b = le16(b, 1) // mono
	b = le32(b, 16000)
	b = le32(b, 16000*2)
	b = le16(b, 2)
	b = le16(b, 16)
	b = append(b, "data"...)
	b = le32(b, uint32(data))
	return append(b, make([]byte, data)...)
}

func le16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }

func le32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
