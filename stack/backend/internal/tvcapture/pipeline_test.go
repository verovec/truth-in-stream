package tvcapture

import (
	"slices"
	"testing"
	"time"
)

func TestFFmpegArgs(t *testing.T) {
	t.Parallel()
	seg := time.Hour

	tests := []struct {
		name    string
		kind    sourceKind
		archive bool
		want    []string
	}{
		{
			name:    "youtube archive",
			kind:    sourceYouTube,
			archive: true,
			want: []string{
				"-hide_banner", "-loglevel", "warning",
				"-i", "pipe:0",
				"-map", "0:a", "-f", "s16le", "-ar", "16000", "-ac", "1", "pipe:1",
				"-map", "0:v?", "-map", "0:a", "-c", "copy",
				"-f", "segment", "-segment_time", "3600", "-strftime", "1",
				"-segment_format", "mpegts", "/work/tf1/%Y%m%d_%H%M%S.ts",
			},
		},
		{
			name:    "hls archive reads manifest directly",
			kind:    sourceHLS,
			archive: true,
			want: []string{
				"-hide_banner", "-loglevel", "warning",
				"-i", "https://example.com/live.m3u8",
				"-map", "0:a", "-f", "s16le", "-ar", "16000", "-ac", "1", "pipe:1",
				"-map", "0:v?", "-map", "0:a", "-c", "copy",
				"-f", "segment", "-segment_time", "3600", "-strftime", "1",
				"-segment_format", "mpegts", "/work/tf1/%Y%m%d_%H%M%S.ts",
			},
		},
		{
			name:    "no archive omits segment output",
			kind:    sourceHLS,
			archive: false,
			want: []string{
				"-hide_banner", "-loglevel", "warning",
				"-i", "https://example.com/live.m3u8",
				"-map", "0:a", "-f", "s16le", "-ar", "16000", "-ac", "1", "pipe:1",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ffmpegArgs("tf1", "https://example.com/live.m3u8", tc.kind, tc.archive, seg, "/work")
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ffmpegArgs()\n got: %v\nwant: %v", got, tc.want)
			}
		})
	}
}

func TestStreamlinkArgs(t *testing.T) {
	t.Parallel()
	got := streamlinkArgs("https://youtube.com/watch?v=abc")
	want := []string{"--stdout", "https://youtube.com/watch?v=abc", "best"}
	if !slices.Equal(got, want) {
		t.Fatalf("streamlinkArgs() = %v, want %v", got, want)
	}
}

func TestRemuxArgs(t *testing.T) {
	t.Parallel()
	got := remuxArgs("/work/tf1/seg.ts", "/work/tf1/seg.mp4")
	// -y must lead so a re-remux over an existing .mp4 overwrites rather than
	// stalling ffmpeg on "Not overwriting - exiting".
	if len(got) == 0 || got[0] != "-y" {
		t.Fatalf("remuxArgs first arg = %v, want -y as args[0]", got)
	}
	if !slices.Contains(got, "-y") {
		t.Fatalf("remuxArgs missing -y: %v", got)
	}
	want := []string{
		"-y",
		"-hide_banner", "-loglevel", "warning",
		"-i", "/work/tf1/seg.ts",
		"-c", "copy",
		"-movflags", "+faststart",
		"/work/tf1/seg.mp4",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remuxArgs()\n got: %v\nwant: %v", got, want)
	}
}

func TestParseSegmentTime(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 7, 13, 14, 5, 9, 0, time.UTC)
	got, err := parseSegmentTime("/work/tf1/20260713_140509.ts")
	if err != nil {
		t.Fatalf("parseSegmentTime: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("parseSegmentTime = %v, want %v", got, want)
	}
	if got.Location() != time.UTC {
		t.Fatalf("parseSegmentTime location = %v, want UTC", got.Location())
	}

	if _, err := parseSegmentTime("/work/tf1/not-a-timestamp.ts"); err == nil {
		t.Fatal("parseSegmentTime: expected error for garbage filename")
	}
}
