package transcribe

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// objectStore is the slice of the media store the object transcriber consumes: a
// single server-side download. Defined here, on the consumer side, so the
// package depends on no storage adapter.
type objectStore interface {
	Download(ctx context.Context, key string) (io.ReadCloser, error)
}

// ObjectTranscriber resolves a source that is a storage object key, downloads
// the object, and transcribes its stream. It is the storage-backed twin of
// SourceTranscriber: uploaded and YouTube-ingested videos keep their bytes in
// object storage, not the bundled media root, and drop into the same pipeline.
type ObjectTranscriber struct {
	transcriber fileTranscriber
	store       objectStore
}

// NewObjectTranscriber builds an ObjectTranscriber that reads objects from store
// and transcribes them with transcriber.
func NewObjectTranscriber(transcriber fileTranscriber, store objectStore) *ObjectTranscriber {
	return &ObjectTranscriber{transcriber: transcriber, store: store}
}

// Transcribe downloads the object named by source and returns its ordered,
// timestamped segments. The download streams straight into the provider, so the
// bytes are never buffered in memory.
func (o *ObjectTranscriber) Transcribe(ctx context.Context, source string) ([]domain.Segment, error) {
	rc, err := o.store.Download(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("transcribe: download object %q: %w", source, err)
	}
	defer func() { _ = rc.Close() }()

	transcript, err := o.transcriber.TranscribeFile(ctx, rc, Options{Filename: path.Base(source)})
	if err != nil {
		return nil, fmt.Errorf("transcribe object %q: %w", source, err)
	}
	return toSegments(transcript), nil
}

// sourceResolver is one transcription strategy: a processing source string to
// domain segments. Both SourceTranscriber and ObjectTranscriber satisfy it.
type sourceResolver interface {
	Transcribe(ctx context.Context, source string) ([]domain.Segment, error)
}

// Router dispatches a processing source to the resolver that can read it: a bare
// filename (the bundled demo clip) to the media-root transcriber, an object key
// to the storage-backed transcriber. The split is by source shape - an object
// key carries a path separator, a bundled filename never does - so the
// processing pipeline stays unaware of where a video's bytes live.
type Router struct {
	local  sourceResolver
	object sourceResolver
}

// NewRouter builds a Router over the bundled-media and object-storage resolvers.
func NewRouter(local, object sourceResolver) *Router {
	return &Router{local: local, object: object}
}

// Transcribe routes source to the matching resolver.
func (r *Router) Transcribe(ctx context.Context, source string) ([]domain.Segment, error) {
	if strings.Contains(source, "/") {
		return r.object.Transcribe(ctx, source)
	}
	return r.local.Transcribe(ctx, source)
}
