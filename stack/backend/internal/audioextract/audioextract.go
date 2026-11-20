// Package audioextract turns a stored video into the paced stream of 16 kHz
// s16le mono PCM frames AssemblyAI's realtime WebSocket expects. ffmpeg
// (already in the backend images for yt-dlp) decodes the object to raw PCM on
// stdout, a chunker slices the stream into 100 ms frames - AssemblyAI closes
// the session with code 3007 on frames outside 50-1000 ms - and a pacer
// releases them at the configured multiple of realtime. The package knows
// nothing about videos, jobs, or HTTP: the pre-analysis job (and any CLI
// debugging tool) consumes a plain frame channel.
package audioextract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// PCM format constants, pinned to what the AssemblyAI live path already
// streams (internal/transcribe): signed 16-bit little-endian, mono, 16 kHz.
// FrameMillis is the emitted frame size; MinFrameMillis is AssemblyAI's floor,
// below which a trailing buffer is dropped rather than sent. BytesPerMilli is
// exported so the pre-analysis job can convert delivered frame lengths into
// audio progress.
const (
	SampleRateHz   = 16000
	BytesPerMilli  = SampleRateHz * bytesPerSample / 1000
	FrameMillis    = 100
	MinFrameMillis = 50

	bytesPerSample = 2
	channels       = 1
	frameBytes     = FrameMillis * BytesPerMilli
	minFrameBytes  = MinFrameMillis * BytesPerMilli
)

// DefaultPacingFactor submits audio at realtime, the only rate AssemblyAI's
// streaming API documents as supported: its transmission-rate check closes a
// session that receives more seconds of audio than have elapsed on the wall
// clock. See internal/config's PREANALYSIS_PACING_FACTOR documentation.
const DefaultPacingFactor = 1.0

// defaultBinary is the ffmpeg executable resolved from PATH when no explicit
// path is configured.
const defaultBinary = "ffmpeg"

// maxStderr bounds how much of ffmpeg's stderr tail is kept for the error;
// ffmpeg prints the fatal diagnostic last, so the tail is the useful part.
const maxStderr = 4096

// waitDelay bounds how long Wait blocks on the I/O pipes after the process is
// killed, so a pathological child holding the stderr pipe open can never hang
// the run goroutine (ffmpeg spawns no children; this is defense in depth).
const waitDelay = 5 * time.Second

// Config configures an Extractor. BinaryPath overrides the PATH lookup of
// ffmpeg. PacingFactor is the multiple of realtime at which frames are
// released (0 means DefaultPacingFactor); values above 1.0 are mechanically
// supported for tests and future provider changes, but the config layer
// rejects them because AssemblyAI documents faster-than-realtime submission
// as a session-closing violation.
type Config struct {
	BinaryPath   string
	PacingFactor float64
}

// Extractor runs ffmpeg to decode stored media into paced PCM frames.
type Extractor struct {
	binary string
	factor float64
	clk    clock
}

// New builds an Extractor, defaulting the binary to "ffmpeg" on PATH and the
// pacing factor to DefaultPacingFactor. The factor must lie in [1e-3, 1e6]:
// the bounds reject NaN and infinities, and the lower one keeps the pacing
// target arithmetic (audio ms divided by factor, in nanoseconds) far from
// int64 duration overflow on long videos.
func New(cfg Config) (*Extractor, error) {
	binary := cfg.BinaryPath
	if binary == "" {
		binary = defaultBinary
	}
	factor := cfg.PacingFactor
	if factor == 0 {
		factor = DefaultPacingFactor
	}
	// The inverted comparison also rejects NaN, which would poison every
	// pacing target.
	if !(factor >= 1e-3 && factor <= 1e6) {
		return nil, fmt.Errorf("audioextract: pacing factor %v outside [1e-3, 1e6]", factor)
	}
	return &Extractor{binary: binary, factor: factor, clk: realClock{}}, nil
}

// Source is one ffmpeg input: a local path or an HTTP(S) URL. Header carries
// request headers replayed on an HTTP fetch (a presigned download's signed
// headers) and must be empty for non-HTTP inputs.
type Source struct {
	Input  string
	Header map[string][]string
}

// MediaPresigner is the slice of domain.MediaStore this package consumes: it
// presigns a download so ffmpeg can fetch the stored object itself.
type MediaPresigner interface {
	PresignDownload(ctx context.Context, key string) (domain.PresignedRequest, error)
}

// PresignedSource opens a stored media object for extraction by presigning a
// download against the media store: ffmpeg then streams the object over
// HTTP(S) itself, so the backend never buffers the video. Signed headers are
// replayed on the fetch except Host, which ffmpeg derives from the URL.
func PresignedSource(ctx context.Context, store MediaPresigner, key string) (Source, error) {
	req, err := store.PresignDownload(ctx, key)
	if err != nil {
		return Source{}, fmt.Errorf("audioextract: presign %q: %w", key, err)
	}
	return Source{Input: req.URL, Header: req.SignedHeaders}, nil
}

// FFmpegError reports an ffmpeg invocation that could not start or exited
// unsuccessfully. Stderr carries the tail of ffmpeg's diagnostics and is
// empty when the process never started.
type FFmpegError struct {
	Err    error
	Stderr string
}

// Error renders the failure with the diagnostic tail when one was captured.
func (e *FFmpegError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("audioextract: ffmpeg: %v", e.Err)
	}
	return fmt.Sprintf("audioextract: ffmpeg: %v: %s", e.Err, e.Stderr)
}

// Unwrap exposes the underlying exec error for errors.Is/As chains.
func (e *FFmpegError) Unwrap() error { return e.Err }

// Stream is one running extraction. Frames delivers paced PCM frames, each
// within AssemblyAI's 50-1000 ms bound, and closes when the media is fully
// decoded, the run fails, or ctx is canceled; Err then reports why. The
// consumer must drain Frames or cancel ctx, or the producer goroutine blocks
// on the unbuffered channel.
type Stream struct {
	frames chan []byte
	err    error
}

// Frames is the paced PCM frame channel. Each frame is a freshly allocated
// slice the consumer may retain.
func (s *Stream) Frames() <-chan []byte { return s.frames }

// Err reports why Frames closed: nil for a fully decoded stream, a
// *FFmpegError for a failed run, or the context error after cancellation. It
// is valid only once Frames is closed (the close happens after the error is
// recorded, so observing the close synchronizes the read).
func (s *Stream) Err() error { return s.err }

// Extract starts ffmpeg on src and returns the paced frame stream. A start
// failure (missing or unrunnable binary) is reported synchronously as a
// *FFmpegError; failures after start (bad or corrupt media) surface through
// Stream.Err after Frames closes. Canceling ctx kills ffmpeg and closes the
// stream.
func (e *Extractor) Extract(ctx context.Context, src Source) (*Stream, error) {
	if src.Input == "" {
		return nil, errors.New("audioextract: empty source input")
	}
	cmd := exec.CommandContext(ctx, e.binary, buildArgs(src)...)
	cmd.WaitDelay = waitDelay
	stderr := &tailBuffer{max: maxStderr}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("audioextract: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, &FFmpegError{Err: fmt.Errorf("start: %w", err)}
	}

	s := &Stream{frames: make(chan []byte)}
	go e.run(ctx, cmd, stdout, stderr, s)
	return s, nil
}

// run pumps ffmpeg's stdout into the frame channel, reaps the process, records
// the terminal error, and closes the channel - in that order, so a consumer
// that observed the close reads a settled Err and never races the process
// teardown. All stdout reads complete before Wait, per the StdoutPipe
// contract.
func (e *Extractor) run(ctx context.Context, cmd *exec.Cmd, stdout io.Reader, stderr *tailBuffer, s *Stream) {
	pumpErr := e.pump(ctx, stdout, s.frames)
	if pumpErr != nil {
		// The consumer is gone: stop ffmpeg now instead of letting it decode
		// the rest of the media into a dead pipe. Killing an already-exited
		// process is a no-op error.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		s.err = fmt.Errorf("audioextract: %w", ctx.Err())
	case pumpErr != nil:
		s.err = pumpErr
	case waitErr != nil:
		s.err = &FFmpegError{Err: waitErr, Stderr: stderr.String()}
	}
	close(s.frames)
}

// pump slices the PCM stream into fixed 100 ms frames and delivers each at
// its pacing target. At end of stream a sample-aligned trailing buffer is
// flushed if it meets AssemblyAI's 50 ms floor and dropped otherwise - the
// same tail rule the live path applies on teardown.
func (e *Extractor) pump(ctx context.Context, r io.Reader, frames chan<- []byte) error {
	p := newPacer(e.clk, e.factor)
	for {
		buf := make([]byte, frameBytes)
		n, err := io.ReadFull(r, buf)
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, io.ErrUnexpectedEOF):
			tail := buf[:n-n%bytesPerSample]
			if len(tail) < minFrameBytes {
				return nil
			}
			return deliver(ctx, p, frames, tail)
		case err != nil:
			return fmt.Errorf("audioextract: read pcm: %w", err)
		}
		if err := deliver(ctx, p, frames, buf); err != nil {
			return err
		}
	}
}

// deliver waits for the frame's pacing target, then sends it unless ctx ends
// first.
func deliver(ctx context.Context, p *pacer, frames chan<- []byte, frame []byte) error {
	if err := p.wait(ctx, len(frame)/BytesPerMilli); err != nil {
		return fmt.Errorf("audioextract: pacing: %w", err)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("audioextract: %w", ctx.Err())
	case frames <- frame:
		return nil
	}
}

// buildArgs assembles the ffmpeg argv: quiet logging, optional HTTP headers
// for the input, then the first audio stream mapped to raw 16 kHz mono s16le
// on stdout. Mapping 0:a:0 (not a bare audio map) matters because the s16le
// muxer takes exactly one stream and broadcast media often carries several
// audio tracks. ffmpeg consumes the argument after -i as the input
// unconditionally, so a hostile input value cannot be read as a flag.
func buildArgs(src Source) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if block := headerBlock(src.Header); block != "" {
		args = append(args, "-headers", block)
	}
	return append(
		args,
		"-i", src.Input,
		"-map", "0:a:0",
		"-f", "s16le",
		"-ar", strconv.Itoa(SampleRateHz),
		"-ac", strconv.Itoa(channels),
		"pipe:1",
	)
}

// headerBlock renders headers as the CRLF-joined block ffmpeg's -headers
// option takes, in sorted key order so the argv is deterministic. Host is
// skipped: ffmpeg sets it from the URL, and a presigned signature is bound to
// that same host.
func headerBlock(header map[string][]string) string {
	if len(header) == 0 {
		return ""
	}
	keys := make([]string, 0, len(header))
	for k := range header {
		if strings.EqualFold(k, "host") {
			continue
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range header[k] {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

// tailBuffer keeps the last max bytes written, so a chatty ffmpeg run cannot
// grow the diagnostic capture without bound while the tail - where ffmpeg
// prints the fatal message - is preserved. It is written by exec's stderr
// goroutine and read only after Wait has joined it.
type tailBuffer struct {
	max       int
	buf       []byte
	truncated bool
}

// Write appends p, evicting the oldest bytes beyond the cap.
func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= b.max {
		b.buf = append(b.buf[:0], p[n-b.max:]...)
		b.truncated = true
		return n, nil
	}
	if overflow := len(b.buf) + n - b.max; overflow > 0 {
		b.buf = b.buf[:copy(b.buf, b.buf[overflow:])]
		b.truncated = true
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

// String renders the captured tail, marking an evicted prefix.
func (b *tailBuffer) String() string {
	s := strings.TrimSpace(string(b.buf))
	if b.truncated {
		return "..." + s
	}
	return s
}
