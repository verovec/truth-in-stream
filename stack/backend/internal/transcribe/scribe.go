package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// ElevenLabs exposes no official Go SDK, so Client is a direct net/http
// adapter. Verified against elevenlabs.io/docs/api-reference/speech-to-text
// (2026-06): POST https://api.elevenlabs.io/v1/speech-to-text, auth header
// xi-api-key, multipart fields file/model_id/timestamps_granularity/diarize/
// tag_audio_events/language_code, response {language_code, words:[{text,
// start, end, type}]} with start/end null on spacing entries. Batch model is
// scribe_v2; the realtime path is scribe_v2_realtime over
// wss://api.elevenlabs.io/v1/speech-to-text/realtime.
const (
	defaultBaseURL = "https://api.elevenlabs.io/v1/speech-to-text"
	// defaultTimeout bounds upload plus provider-side processing of long
	// recordings, not a single round-trip.
	defaultTimeout  = 5 * time.Minute
	defaultFilename = "audio"
)

const (
	wordTypeWord       = "word"
	wordTypeSpacing    = "spacing"
	wordTypeAudioEvent = "audio_event"
)

// Segment grouping bounds: a sentence-final word always closes a segment; a
// silence longer than maxPause or a segment reaching maxSegmentDuration closes
// it too, so unpunctuated speech still yields spans short enough to match.
const (
	maxPause           = time.Second
	maxSegmentDuration = 30 * time.Second
)

// Config configures a Client. APIKey and Model are required; BaseURL and
// HTTPClient default to the ElevenLabs endpoint and a client whose timeout
// covers long uploads.
type Config struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

// Client calls the ElevenLabs Scribe speech-to-text API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
}

// New builds a Client from cfg, applying defaults for the optional fields.
func New(cfg Config) *Client {
	c := &Client{
		httpClient: cfg.HTTPClient,
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	return c
}

// ErrRateLimited marks a provider-side rate limit, matched with errors.Is.
// It is transport-agnostic on purpose: the realtime path signals rate limits
// without HTTP statuses, so callers must not key off APIError.StatusCode.
var ErrRateLimited = errors.New("transcribe: provider rate limited")

// APIError is a non-2xx response from the Scribe API. Callers match it with
// errors.AsType to map provider failures (auth, rate limit, validation) to
// their own statuses.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("scribe: api status %d: %s", e.StatusCode, e.Body)
}

type scribeWord struct {
	Text  string   `json:"text"`
	Start *float64 `json:"start"`
	End   *float64 `json:"end"`
	Type  string   `json:"type"`
}

type scribeResponse struct {
	LanguageCode string       `json:"language_code"`
	Words        []scribeWord `json:"words"`
}

// TranscribeFile uploads the complete source as multipart form data and groups
// the returned word timestamps into segments.
func (c *Client) TranscribeFile(ctx context.Context, audio io.Reader, opts Options) (Transcript, error) {
	body, contentType := c.formBody(audio, opts)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, body)
	if err != nil {
		body.CloseWithError(err)
		return Transcript{}, fmt.Errorf("scribe: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("xi-api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Transcript{}, fmt.Errorf("scribe: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		apiErr := &APIError{StatusCode: resp.StatusCode, Body: string(detail)}
		if resp.StatusCode == http.StatusTooManyRequests {
			return Transcript{}, fmt.Errorf("%w: %w", ErrRateLimited, apiErr)
		}
		return Transcript{}, apiErr
	}

	var decoded scribeResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return Transcript{}, fmt.Errorf("scribe: decode response: %w", err)
	}
	// Drain the trailing bytes the decoder did not read so the transport can
	// reuse the connection.
	_, _ = io.Copy(io.Discard, resp.Body)
	return Transcript{
		Language: decoded.LanguageCode,
		Segments: groupWords(decoded.Words),
	}, nil
}

// TranscribeStream is the live-mode half of the Transcriber contract, to be
// implemented against Scribe v2 Realtime. v1 is batch-only.
func (c *Client) TranscribeStream(_ context.Context, _ <-chan []byte, _ Options) (<-chan TranscriptEvent, error) {
	return nil, errors.New("scribe: streaming transcription is not implemented in v1")
}

// formBody streams the multipart request body: the form is written through a
// pipe so the audio is never buffered in memory. Callers that fail before
// handing the reader to the transport must close it or the writer goroutine
// blocks forever.
func (c *Client) formBody(audio io.Reader, opts Options) (*io.PipeReader, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(c.writeForm(mw, audio, opts))
	}()
	return pr, mw.FormDataContentType()
}

type formField struct {
	name  string
	value string
}

func (c *Client) writeForm(mw *multipart.Writer, audio io.Reader, opts Options) error {
	fields := []formField{
		{"model_id", c.model},
		{"timestamps_granularity", "word"},
		{"diarize", "false"},
		{"tag_audio_events", "false"},
	}
	if opts.Language != "" {
		fields = append(fields, formField{"language_code", opts.Language})
	}
	for _, f := range fields {
		if err := mw.WriteField(f.name, f.value); err != nil {
			return fmt.Errorf("scribe: write field %s: %w", f.name, err)
		}
	}

	filename := opts.Filename
	if filename == "" {
		filename = defaultFilename
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return fmt.Errorf("scribe: create file part: %w", err)
	}
	if _, err := io.Copy(part, audio); err != nil {
		return fmt.Errorf("scribe: copy audio: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("scribe: close form: %w", err)
	}
	return nil
}

// groupWords folds word-level timestamps into ordered segments. Spacing
// entries contribute their whitespace to the running text, audio events are
// dropped, and untimed words contribute text without moving the boundaries.
func groupWords(words []scribeWord) []Segment {
	segments := make([]Segment, 0, len(words)/8+1)
	var text strings.Builder
	var start, end time.Duration
	timed := false

	flush := func() {
		t := strings.TrimSpace(text.String())
		text.Reset()
		if t != "" {
			segments = append(segments, Segment{Start: start, End: end, Text: t})
		}
		start, end = 0, 0
		timed = false
	}

	for _, w := range words {
		switch w.Type {
		case wordTypeAudioEvent:
			continue
		case wordTypeSpacing:
			text.WriteString(w.Text)
			continue
		}
		if timed && w.Start != nil && seconds(*w.Start)-end > maxPause {
			flush()
		}
		text.WriteString(w.Text)
		if w.Start != nil && !timed {
			start = seconds(*w.Start)
			timed = true
		}
		if w.End != nil {
			end = seconds(*w.End)
		}
		if endsSentence(w.Text) || (timed && end-start >= maxSegmentDuration) {
			flush()
		}
	}
	flush()
	return segments
}

func endsSentence(text string) bool {
	r, _ := utf8.DecodeLastRuneInString(text)
	return r == '.' || r == '!' || r == '?'
}

func seconds(sec float64) time.Duration {
	return time.Duration(sec * float64(time.Second))
}
