package transcribe

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// streamClient is the slice of the streaming provider the live adapter
// consumes: audio chunks in, transcript events out.
type streamClient interface {
	TranscribeStream(ctx context.Context, chunks <-chan []byte, opts Options) (<-chan TranscriptEvent, error)
}

// StreamSegmenter adapts the streaming provider to the live pipeline's
// transcript port. It maps every transcript event to a domain.LiveTranscript,
// tagging committed events Final and partials non-final: the live pipeline
// surfaces partials as interim captions and only fact-checks the finalized
// ones.
type StreamSegmenter struct {
	client streamClient
	opts   Options
}

// NewStreamSegmenter builds a StreamSegmenter that transcribes streaming audio
// with client under the given options.
func NewStreamSegmenter(client streamClient, opts Options) *StreamSegmenter {
	return &StreamSegmenter{client: client, opts: opts}
}

// StreamSegments transcribes audio as it arrives and emits one
// domain.LiveTranscript per transcript event - partials as they are revised,
// committed segments as the provider finalizes them. The returned channel closes
// when audio closes, the provider ends the session, or ctx is canceled.
func (s *StreamSegmenter) StreamSegments(ctx context.Context, audio <-chan []byte) (<-chan domain.LiveTranscript, error) {
	events, err := s.client.TranscribeStream(ctx, audio, s.opts)
	if err != nil {
		return nil, err
	}
	out := make(chan domain.LiveTranscript)
	go func() {
		defer close(out)
		for ev := range events {
			tr := domain.LiveTranscript{
				Segment: domain.Segment{Start: ev.Segment.Start, End: ev.Segment.End, Text: ev.Segment.Text, Speaker: ev.Segment.Speaker},
				Final:   ev.Final,
			}
			select {
			case <-ctx.Done():
				return
			case out <- tr:
			}
		}
	}()
	return out, nil
}
