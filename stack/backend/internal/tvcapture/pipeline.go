package tvcapture

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// sourceKind is a channel's upstream transport: YouTube (fetched via streamlink)
// or a direct HLS manifest (read by ffmpeg directly).
type sourceKind string

const (
	sourceYouTube sourceKind = "youtube"
	sourceHLS     sourceKind = "hls"
)

// segmentLayout is the strftime template ffmpeg writes segment files with; it is
// also the layout parseSegmentTime reads back. Kept as one constant so the writer
// and reader can never drift.
const (
	segmentTimeLayout = "20060102_150405"
	segmentExt        = ".ts"
	segmentPattern    = "%Y%m%d_%H%M%S" + segmentExt
)

// pcmSampleRate, pcmChannels and the derived frame size define the audio the
// analyzer expects: 16 kHz mono signed 16-bit little-endian PCM.
const (
	pcmSampleRate = 16000
	pcmChannels   = 1
	// pcmFrameBytes is 100 ms of audio: 16000 samples/s * 0.1s * 2 bytes/sample.
	pcmFrameBytes = pcmSampleRate / 10 * 2 * pcmChannels
)

// streamlinkArgs returns the streamlink argument vector that writes the best
// quality of source to stdout for ffmpeg to consume on pipe:0.
func streamlinkArgs(source string) []string {
	return []string{"--stdout", source, "best"}
}

// ffmpegArgs builds the ffmpeg argument vector for a channel's capture. It always
// maps the audio to a 16 kHz mono s16le stream on stdout (pipe:1). When archive
// is set it also stream-copies the source into strftime-named MPEG-TS segments of
// segment length under workDir/<slug>. For youtube the input is streamlink's
// stdout (pipe:0); for hls ffmpeg reads the manifest directly.
func ffmpegArgs(slug, source string, kind sourceKind, archive bool, segment time.Duration, workDir string) []string {
	args := []string{"-hide_banner", "-loglevel", "warning"}

	if kind == sourceYouTube {
		args = append(args, "-i", "pipe:0")
	} else {
		args = append(args, "-i", source)
	}

	// PCM output to stdout for the live analyzer. Map only the first audio stream
	// (0:a:0): the s16le raw muxer takes exactly one stream, and simulcasts often
	// carry several audio tracks (audio description, secondary language), which a
	// bare 0:a would map all of and fail on.
	args = append(
		args,
		"-map", "0:a:0",
		"-f", "s16le",
		"-ar", strconv.Itoa(pcmSampleRate),
		"-ac", strconv.Itoa(pcmChannels),
		"pipe:1",
	)

	if archive {
		segSeconds := strconv.Itoa(int(segment.Seconds()))
		target := filepath.Join(workDir, slug, segmentPattern)
		args = append(
			args,
			"-map", "0:v?",
			"-map", "0:a",
			"-c", "copy",
			"-f", "segment",
			"-segment_time", segSeconds,
			"-strftime", "1",
			"-segment_format", "mpegts",
			target,
		)
	}

	return args
}

// remuxArgs builds the ffmpeg argument vector that remuxes a TS segment to a
// faststart MP4 without re-encoding. -y makes the remux overwrite idempotently:
// a retried archive (segment re-globbed and re-remuxed) must not stall on ffmpeg
// refusing to overwrite an existing .mp4.
func remuxArgs(tsPath, mp4Path string) []string {
	return []string{
		"-y",
		"-hide_banner", "-loglevel", "warning",
		"-i", tsPath,
		"-c", "copy",
		"-movflags", "+faststart",
		mp4Path,
	}
}

// parseSegmentTime reads the capture time from a segment filename written with
// segmentPattern, interpreting it as UTC (ffmpeg runs with TZ=UTC).
func parseSegmentTime(filename string) (time.Time, error) {
	base := filepath.Base(filename)
	name := base[:len(base)-len(filepath.Ext(base))]
	t, err := time.ParseInLocation(segmentTimeLayout, name, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("tvcapture: parse segment time %q: %w", base, err)
	}
	return t, nil
}
