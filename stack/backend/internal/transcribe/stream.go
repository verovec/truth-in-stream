package transcribe

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// streamClient is the slice of *Client the live adapter consumes: the streaming
// half of the Transcriber contract.
type streamClient interface {
	TranscribeStream(ctx context.Context, chunks <-chan []byte, opts Options) (<-chan TranscriptEvent, error)
}

// StreamSegmenter adapts the streaming provider Transcriber to the live
// pipeline's finalized-segment port. It drops non-final (partial) events and
// maps each committed transcript to a domain.Segment: partials are interim
// revisions the fact-check pipeline cannot act on, so only finalized segments
// cross into the service layer. It mirrors SourceTranscriber, which adapts the
// batch path to the same domain shape.
type StreamSegmenter struct {
	client streamClient
	opts   Options
}

// NewStreamSegmenter builds a StreamSegmenter that transcribes streaming audio
// with client under the given options.
func NewStreamSegmenter(client streamClient, opts Options) *StreamSegmenter {
	return &StreamSegmenter{client: client, opts: opts}
}

// StreamSegments transcribes audio as it arrives and emits one domain.Segment
// per finalized transcript. The returned channel closes when audio closes, the
// provider ends the session, or ctx is canceled.
func (s *StreamSegmenter) StreamSegments(ctx context.Context, audio <-chan []byte) (<-chan domain.Segment, error) {
	events, err := s.client.TranscribeStream(ctx, audio, s.opts)
	if err != nil {
		return nil, err
	}
	out := make(chan domain.Segment)
	go func() {
		defer close(out)
		for ev := range events {
			if !ev.Final {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- domain.Segment{Start: ev.Segment.Start, End: ev.Segment.End, Text: ev.Segment.Text}:
			}
		}
	}()
	return out, nil
}
