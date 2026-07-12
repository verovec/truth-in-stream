package tvcapture

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestCapturePipelineIntegration drives the real ffmpeg dual-output pipeline end
// to end against a locally generated fixture: it captures PCM to stdout AND
// stream-copies segmented TS to disk, then archives one closed segment through
// the real ffmpeg remux and a fake uploader. It is skip-clean: with no ffmpeg on
// PATH (or an ffmpeg lacking the fixture encoders) it skips rather than fails, so
// CI without ffmpeg stays green. The full worker-to-backend capture (feed WS,
// recording registration against the live hub) is exercised by the compose
// fixture-stream run documented in the PR, not here.
func TestCapturePipelineIntegration(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH; skipping capture pipeline integration test")
	}

	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.ts")
	// A 3s MPEG-TS with a real audio track (and video, so the stream-copy segment
	// output has a video stream to copy, matching a live simulcast).
	gen := exec.CommandContext(t.Context(), ffmpeg, "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=3",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15:duration=3",
		"-c:a", "aac", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-shortest",
		"-f", "mpegts", fixture)
	if out, genErr := gen.CombinedOutput(); genErr != nil {
		t.Skipf("ffmpeg cannot generate the fixture (missing encoders?): %v\n%s", genErr, out)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	workDir := filepath.Join(dir, "work")
	ch := Channel{ID: "c1", Slug: "fixture", Name: "Fixture", SourceKind: "hls", SourceRef: fixture, ArchiveEnabled: true}
	spec := captureSpec{Channel: ch, WorkDir: workDir, Segment: time.Second, Archive: true, FFmpegPath: ffmpeg}
	// The supervisor creates the per-channel segment dir before starting ffmpeg;
	// this test drives the runner directly, so create it here too.
	if err := os.MkdirAll(filepath.Join(workDir, ch.Slug), 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	proc, err := newExecRunner(logger).Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start capture: %v", err)
	}

	var pcmBytes int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(io.Discard, proc.PCM())
		pcmBytes = n
	}()
	_ = proc.Wait() // finite fixture: ffmpeg exits on its own
	wg.Wait()

	// 16 kHz mono s16le is 32000 bytes/s; a 3s clip yields ~96 KB. Require at
	// least ~0.5s so we know real audio flowed, not an empty pipe.
	if pcmBytes < 16000 {
		t.Fatalf("captured %d PCM bytes, want a real 16 kHz mono stream", pcmBytes)
	}

	segments, err := filepath.Glob(filepath.Join(workDir, ch.Slug, "*"+segmentExt))
	if err != nil {
		t.Fatalf("glob segments: %v", err)
	}
	if len(segments) == 0 {
		t.Fatalf("no TS segments were written to %s", filepath.Join(workDir, ch.Slug))
	}

	// Archive one closed segment through the real ffmpeg remux and a fake uploader.
	up := &fakeUploader{}
	arch := newArchiver(up, ffmpeg, logger)
	if err := arch.archive(context.Background(), ch, segments[0]); err != nil {
		t.Fatalf("archive segment: %v", err)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.sizeBytes <= 0 {
		t.Errorf("archived recording size = %d, want > 0", up.sizeBytes)
	}
	if len(up.uploaded) != 1 || len(up.registered) != 1 {
		t.Errorf("uploaded=%d registered=%d, want 1 each", len(up.uploaded), len(up.registered))
	}
	// The remuxed MP4 is removed after a successful upload.
	if leftovers, _ := filepath.Glob(filepath.Join(workDir, ch.Slug, "*.mp4")); len(leftovers) != 0 {
		t.Errorf("mp4 not cleaned up after archive: %v", leftovers)
	}
}
