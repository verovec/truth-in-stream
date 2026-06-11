// Package ytdlp downloads videos with the yt-dlp command-line tool. It is the
// download adapter behind the ingest service's Downloader port: yt-dlp is the
// actively maintained standard that absorbs YouTube's format and throttling
// changes a hand-rolled scraper cannot. The binary is invoked as a subprocess
// with an explicit argv - the operator URL is never interpolated into a shell
// string - so a malicious link cannot inject a command. It exposes no HTTP types
// and returns the shared domain.DownloadResult, so it satisfies the service port
// structurally without importing it.
package ytdlp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// defaultBinary is the yt-dlp executable resolved from PATH when no explicit
// path is configured.
const defaultBinary = "yt-dlp"

// outputContentType is the container the downloader remuxes to, and the content
// type the ingest records on the stored object.
const outputContentType = "video/mp4"

// maxStderr bounds how much of a failed run's stderr is wrapped into the error,
// keeping logs and error chains from carrying a runaway diagnostic dump.
const maxStderr = 4096

// Config configures a Downloader. BinaryPath overrides the PATH lookup of
// yt-dlp; MaxBytes caps a single download and is passed to yt-dlp's
// --max-filesize so an oversized video is rejected before it is fully fetched.
type Config struct {
	BinaryPath string
	MaxBytes   int64
}

// Downloader runs yt-dlp to fetch a single video to a destination directory.
type Downloader struct {
	binary   string
	maxBytes int64
}

// New builds a Downloader, defaulting the binary to "yt-dlp" on PATH.
func New(cfg Config) *Downloader {
	binary := cfg.BinaryPath
	if binary == "" {
		binary = defaultBinary
	}
	return &Downloader{binary: binary, maxBytes: cfg.MaxBytes}
}

// Download fetches videoURL into destDir as a single mp4 and returns its path
// and probed metadata. yt-dlp prints the title and duration in the extraction
// phase and the final path in the after-move phase, so stdout is one
// "title<TAB>duration" line followed by the path line.
func (d *Downloader) Download(ctx context.Context, videoURL, destDir string) (domain.DownloadResult, error) {
	cmd := exec.CommandContext(ctx, d.binary, buildArgs(videoURL, destDir, d.maxBytes)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return domain.DownloadResult{}, fmt.Errorf("ytdlp: run: %w: %s", err, truncate(stderr.String()))
	}

	title, durationMS, path, err := parseOutput(stdout.String())
	if err != nil {
		return domain.DownloadResult{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return domain.DownloadResult{}, fmt.Errorf("ytdlp: stat output %q: %w", path, err)
	}

	return domain.DownloadResult{
		FilePath:    path,
		Title:       title,
		DurationMS:  durationMS,
		SizeBytes:   info.Size(),
		ContentType: outputContentType,
	}, nil
}

// buildArgs assembles the yt-dlp argv. The video URL is the final, single
// argument and is never placed in a shell string. The format selector prefers
// the best video+audio and remuxes to mp4 (deterministic .mp4 output) so the
// stored object plays back directly; --no-playlist constrains a list URL to its
// single video; --max-filesize bounds the fetch.
func buildArgs(videoURL, destDir string, maxBytes int64) []string {
	args := []string{
		"--no-playlist",
		"--no-progress",
		"--no-warnings",
		"--no-simulate",
		"-f", "bv*+ba/b",
		"--remux-video", "mp4",
		"-o", destDir + "/%(id)s.%(ext)s",
		"--print", "%(title)s\t%(duration)s",
		"--print", "after_move:%(filepath)s",
	}
	if maxBytes > 0 {
		args = append(args, "--max-filesize", strconv.FormatInt(maxBytes, 10))
	}
	return append(args, "--", videoURL)
}

// parseOutput reads yt-dlp's stdout: a "title<TAB>duration" line from the
// extraction phase and the final file path from the after-move phase. The path
// is the last non-empty line; the metadata line is the first. A missing or
// unparseable duration is treated as unknown (0), not an error.
func parseOutput(out string) (title string, durationMS int64, path string, err error) {
	lines := make([]string, 0, 2)
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if line := scanner.Text(); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) < 2 {
		return "", 0, "", fmt.Errorf("ytdlp: unexpected output %q", out)
	}

	path = lines[len(lines)-1]
	title, durationMS = parseMetaLine(lines[0])
	return title, durationMS, path, nil
}

// parseMetaLine splits the "title<TAB>duration" line. The duration is whole or
// fractional seconds; "NA" or empty (a live or unknown source) yields 0.
func parseMetaLine(line string) (string, int64) {
	title, rawDuration, found := strings.Cut(line, "\t")
	title = strings.TrimSpace(title)
	if !found {
		return title, 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(rawDuration), 64)
	if err != nil || seconds <= 0 {
		return title, 0
	}
	return title, int64(seconds * 1000)
}

// truncate bounds a stderr dump for inclusion in an error.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxStderr {
		return s[:maxStderr] + "..."
	}
	return s
}
