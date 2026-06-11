package transcribe

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fileTranscriber is the slice of *Client the source adapter consumes: a
// complete pre-recorded source transcribed in one call.
type fileTranscriber interface {
	TranscribeFile(ctx context.Context, audio io.Reader, opts Options) (Transcript, error)
}

// SourceTranscriber resolves a video source identifier to bundled media on
// disk, transcribes it, and returns domain segments. It adapts the provider
// Transcriber (which reads an io.Reader) to the processing pipeline's
// source-string port; the v1 batch demo serves pre-recorded clips from a media
// root, and the future live path swaps in a stream-backed resolver behind the
// same pipeline interface.
type SourceTranscriber struct {
	transcriber fileTranscriber
	mediaRoot   string
}

// NewSourceTranscriber builds a SourceTranscriber that reads sources from
// mediaRoot and transcribes them with transcriber.
func NewSourceTranscriber(transcriber fileTranscriber, mediaRoot string) *SourceTranscriber {
	return &SourceTranscriber{transcriber: transcriber, mediaRoot: mediaRoot}
}

// Transcribe reads the media named by source from the media root and returns
// its ordered, timestamped segments. source must be a bare filename; any path
// component is rejected so a source can never escape the media root.
func (s *SourceTranscriber) Transcribe(ctx context.Context, source string) ([]domain.Segment, error) {
	name, err := safeMediaName(source)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(filepath.Join(s.mediaRoot, name))
	if err != nil {
		return nil, fmt.Errorf("transcribe: open media %q: %w", source, err)
	}
	defer func() { _ = f.Close() }()

	transcript, err := s.transcriber.TranscribeFile(ctx, f, Options{Filename: name})
	if err != nil {
		return nil, fmt.Errorf("transcribe source %q: %w", source, err)
	}
	return toSegments(transcript), nil
}

// toSegments maps a provider transcript to the pipeline's domain segments,
// shared by every source resolver so the projection lives in one place.
func toSegments(transcript Transcript) []domain.Segment {
	segments := make([]domain.Segment, 0, len(transcript.Segments))
	for _, seg := range transcript.Segments {
		segments = append(segments, domain.Segment{Start: seg.Start, End: seg.End, Text: seg.Text})
	}
	return segments
}

// safeMediaName accepts only a bare filename, rejecting empty input and any
// path separators or traversal so the resolved path stays inside the media
// root.
func safeMediaName(source string) (string, error) {
	if source == "" {
		return "", fmt.Errorf("transcribe: empty media source")
	}
	if filepath.Base(source) != source || source == "." || source == ".." {
		return "", fmt.Errorf("transcribe: media source %q must be a bare filename", source)
	}
	return source, nil
}
