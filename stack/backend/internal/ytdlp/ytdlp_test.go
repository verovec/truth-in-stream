package ytdlp

import (
	"slices"
	"testing"
)

func TestBuildArgsPassesURLAsFinalArgument(t *testing.T) {
	t.Parallel()
	const url = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	args := buildArgs(url, "/tmp/dest", 1024)

	// The URL is the last argument and is preceded by "--" so a URL beginning
	// with a dash can never be read as a flag - the command-injection guard.
	if got := args[len(args)-1]; got != url {
		t.Errorf("final arg = %q, want the url", got)
	}
	if got := args[len(args)-2]; got != "--" {
		t.Errorf("arg before url = %q, want --", got)
	}
	if !slices.Contains(args, "--no-playlist") {
		t.Error("missing --no-playlist")
	}
	if !slices.Contains(args, "--max-filesize") || !slices.Contains(args, "1024") {
		t.Errorf("size bound not passed: %v", args)
	}
	if !slices.Contains(args, "-o") || !slices.Contains(args, "/tmp/dest/%(id)s.%(ext)s") {
		t.Errorf("output template not set: %v", args)
	}
}

func TestBuildArgsOmitsSizeBoundWhenZero(t *testing.T) {
	t.Parallel()
	args := buildArgs("https://youtu.be/dQw4w9WgXcQ", "/tmp", 0)
	if slices.Contains(args, "--max-filesize") {
		t.Errorf("--max-filesize present with no bound: %v", args)
	}
}

func TestParseOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		out          string
		wantTitle    string
		wantDuration int64
		wantPath     string
		wantErr      bool
	}{
		{
			name:         "title duration and path",
			out:          "Never Gonna Give You Up\t212.5\n/tmp/dest/dQw4w9WgXcQ.mp4\n",
			wantTitle:    "Never Gonna Give You Up",
			wantDuration: 212500,
			wantPath:     "/tmp/dest/dQw4w9WgXcQ.mp4",
		},
		{
			name:         "unknown duration",
			out:          "A Live Stream\tNA\n/tmp/dest/abc.mp4\n",
			wantTitle:    "A Live Stream",
			wantDuration: 0,
			wantPath:     "/tmp/dest/abc.mp4",
		},
		{
			name:         "integer duration",
			out:          "Clip\t90\n/tmp/x.mp4",
			wantTitle:    "Clip",
			wantDuration: 90000,
			wantPath:     "/tmp/x.mp4",
		},
		{
			name:         "title whitespace trimmed",
			out:          "  Padded Title \t120\n/tmp/p.mp4",
			wantTitle:    "Padded Title",
			wantDuration: 120000,
			wantPath:     "/tmp/p.mp4",
		},
		{
			name:    "missing path line",
			out:     "Only Title\t10\n",
			wantErr: true,
		},
		{
			name:    "empty output",
			out:     "",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			title, duration, path, err := parseOutput(tc.out)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
			if duration != tc.wantDuration {
				t.Errorf("duration = %d, want %d", duration, tc.wantDuration)
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
		})
	}
}
