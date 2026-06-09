package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
)

// maxTranscriptUploadBytes bounds the multipart upload; the Scribe API itself
// accepts up to 5 GB but v1 sources are short pre-recorded clips.
const maxTranscriptUploadBytes = 1 << 30

// transcriptRequestBudget extends this route's read and write deadlines past
// the tight server-wide timeouts: the upload plus the provider call share the
// transcribe client's 5-minute budget, with slack for encoding the response.
const transcriptRequestBudget = 6 * time.Minute

type segmentResponse struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type transcriptResponse struct {
	Language string            `json:"language"`
	Segments []segmentResponse `json:"segments"`
}

// transcriptHandler accepts a multipart upload with a "file" part (audio or
// video) and an optional "language" query parameter, and responds with the
// ordered transcript segments.
func transcriptHandler(transcriber transcribe.Transcriber, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		extendDeadlines(w)
		r.Body = http.MaxBytesReader(w, r.Body, maxTranscriptUploadBytes)
		file, err := requestFilePart(r)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "multipart body with a file part is required")
			return
		}
		defer func() { _ = file.Close() }()

		opts := transcribe.Options{
			Language: r.URL.Query().Get("language"),
			Filename: file.FileName(),
		}
		transcript, err := transcriber.TranscribeFile(r.Context(), file, opts)
		if err != nil {
			writeTranscribeError(r.Context(), w, logger, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toTranscriptResponse(transcript))
	}
}

// extendDeadlines lengthens the connection deadlines for this slow route while
// the server keeps tight global timeouts. Failure is tolerated: recorders and
// exotic transports without deadline support just keep the server defaults.
func extendDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(transcriptRequestBudget)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)
}

// requestFilePart advances the multipart stream to the "file" part so the
// upload is handed to the transcriber without buffering the whole body.
func requestFilePart(r *http.Request) (*multipart.Part, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("handler: multipart body: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, fmt.Errorf("handler: reading multipart file part: %w", err)
		}
		if part.FormName() == "file" {
			return part, nil
		}
	}
}

// writeTranscribeError maps failures to explicit statuses: provider rate
// limiting is a retryable 503, a timed-out call is a 504, an upload past the
// size cap is the client's 413, and anything else from the provider side is a
// 502. Details are logged, never exposed.
func writeTranscribeError(ctx context.Context, w http.ResponseWriter, logger *slog.Logger, err error) {
	logger.ErrorContext(ctx, "transcription failed", slog.Any("err", err))

	switch {
	case errors.Is(err, transcribe.ErrRateLimited):
		httpx.Error(w, http.StatusServiceUnavailable, "transcription provider is rate limiting, retry later")
	case errors.Is(err, context.DeadlineExceeded):
		httpx.Error(w, http.StatusGatewayTimeout, "transcription timed out")
	case isMaxBytes(err):
		httpx.Error(w, http.StatusRequestEntityTooLarge, "upload exceeds the size limit")
	default:
		httpx.Error(w, http.StatusBadGateway, "transcription provider failed")
	}
}

// isMaxBytes reports whether the transcriber failed because the size-capped
// request body tripped http.MaxBytesReader while streaming to the provider.
func isMaxBytes(err error) bool {
	_, ok := errors.AsType[*http.MaxBytesError](err)
	return ok
}

func toTranscriptResponse(tr transcribe.Transcript) transcriptResponse {
	segments := make([]segmentResponse, 0, len(tr.Segments))
	for _, s := range tr.Segments {
		segments = append(segments, segmentResponse{
			Start: s.Start.Seconds(),
			End:   s.End.Seconds(),
			Text:  s.Text,
		})
	}
	return transcriptResponse{Language: tr.Language, Segments: segments}
}
