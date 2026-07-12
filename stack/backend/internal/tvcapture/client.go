package tvcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// recordingContentType is the only content type the worker uploads; recordings
// are always remuxed to fragmented MP4 before upload.
const recordingContentType = "video/mp4"

// Channel is a TV channel the worker may capture, as returned by the backend
// registry. SourceKind is "youtube" or "hls"; SourceRef is the stable upstream
// reference (a YouTube URL or an HLS manifest URL) - never a resolved stream URL.
type Channel struct {
	ID             string
	Slug           string
	Name           string
	SourceKind     string
	SourceRef      string
	Enabled        bool
	ArchiveEnabled bool
	Live           bool
}

// tokenProvider yields a bearer token for backend calls.
type tokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// presignedRequest is the storage upload instruction returned by the backend:
// issue Method to URL, replaying every header, with the file bytes as the body.
type presignedRequest struct {
	URL     string
	Method  string
	Headers map[string][]string
}

// recordingTicket is the backend's response to an upload request: the created
// video id, its object key, and the presigned request to store the bytes.
type recordingTicket struct {
	VideoID   string
	ObjectKey string
	Upload    presignedRequest
}

// backendClient calls the backend's admin TV API. Every request carries the
// client-credentials bearer token from tokens.
type backendClient struct {
	baseURL string
	http    *http.Client
	tokens  tokenProvider
}

func newBackendClient(baseURL string, httpClient *http.Client, tokens tokenProvider) *backendClient {
	return &backendClient{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, tokens: tokens}
}

func (c *backendClient) authorize(ctx context.Context, req *http.Request) error {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// ListChannels returns the current TV channel registry.
func (c *backendClient) ListChannels(ctx context.Context) ([]Channel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tv/channels", nil)
	if err != nil {
		return nil, fmt.Errorf("tvcapture: build list-channels request: %w", err)
	}
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tvcapture: list channels: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("list channels", resp)
	}
	var body struct {
		Channels []struct {
			ID             string `json:"id"`
			Slug           string `json:"slug"`
			Name           string `json:"name"`
			SourceKind     string `json:"source_kind"`
			SourceRef      string `json:"source_ref"`
			Enabled        bool   `json:"enabled"`
			ArchiveEnabled bool   `json:"archive_enabled"`
			Live           bool   `json:"live"`
		} `json:"channels"`
	}
	if err := decodeJSON(resp.Body, &body); err != nil {
		return nil, fmt.Errorf("tvcapture: decode channels: %w", err)
	}
	out := make([]Channel, 0, len(body.Channels))
	for _, ch := range body.Channels {
		out = append(out, Channel(ch))
	}
	return out, nil
}

// RequestUpload asks the backend for a presigned upload for a recording of the
// given size captured at recordedAt. The content type is always video/mp4.
func (c *backendClient) RequestUpload(ctx context.Context, channelID string, recordedAt time.Time, sizeBytes int64) (recordingTicket, error) {
	payload := map[string]any{
		"channel_id":   channelID,
		"recorded_at":  recordedAt.UTC().Format(time.RFC3339),
		"content_type": recordingContentType,
		"size_bytes":   sizeBytes,
	}
	resp, err := c.postJSON(ctx, "/api/tv/recordings/uploads", payload)
	if err != nil {
		return recordingTicket{}, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusCreated {
		return recordingTicket{}, statusError("request upload", resp)
	}
	var body struct {
		VideoID   string `json:"video_id"`
		ObjectKey string `json:"object_key"`
		Status    string `json:"status"`
		Upload    struct {
			URL     string              `json:"url"`
			Method  string              `json:"method"`
			Headers map[string][]string `json:"headers"`
		} `json:"upload"`
	}
	if err := decodeJSON(resp.Body, &body); err != nil {
		return recordingTicket{}, fmt.Errorf("tvcapture: decode upload ticket: %w", err)
	}
	return recordingTicket{
		VideoID:   body.VideoID,
		ObjectKey: body.ObjectKey,
		Upload: presignedRequest{
			URL:     body.Upload.URL,
			Method:  body.Upload.Method,
			Headers: body.Upload.Headers,
		},
	}, nil
}

// UploadFile stores the file at path using the ticket's presigned request. A 412
// (precondition failed, e.g. object already present) is treated as success so a
// retried upload is idempotent.
func (c *backendClient) UploadFile(ctx context.Context, tk recordingTicket, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("tvcapture: open recording: %w", err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("tvcapture: stat recording: %w", err)
	}

	method := tk.Upload.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, tk.Upload.URL, f)
	if err != nil {
		return fmt.Errorf("tvcapture: build upload request: %w", err)
	}
	req.ContentLength = info.Size()
	for k, vs := range tk.Upload.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tvcapture: upload recording: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("upload recording", resp)
	}
	return nil
}

// Register finalizes a recording once its bytes are in storage. The backend
// answers 409 while the object is not yet visible; the caller retries.
func (c *backendClient) Register(ctx context.Context, videoID string) error {
	resp, err := c.postJSON(ctx, "/api/tv/recordings", map[string]any{"video_id": videoID})
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return statusError("register recording", resp)
	}
	return nil
}

// Prune asks the backend to delete recordings older than retentionDays and
// returns the number deleted.
func (c *backendClient) Prune(ctx context.Context, retentionDays int) (int, error) {
	resp, err := c.postJSON(ctx, "/api/tv/recordings/prune", map[string]any{"retention_days": retentionDays})
	if err != nil {
		return 0, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return 0, statusError("prune recordings", resp)
	}
	var body struct {
		Deleted int `json:"deleted"`
	}
	if err := decodeJSON(resp.Body, &body); err != nil {
		return 0, fmt.Errorf("tvcapture: decode prune result: %w", err)
	}
	return body.Deleted, nil
}

// FeedURL builds the publisher WebSocket URL for a channel, deriving ws/wss from
// the backend's http/https scheme. The token rides the Authorization header on
// the dial (see feed.go), never the URL, so it cannot leak into logs or proxies.
func (c *backendClient) FeedURL(channelID string) string {
	scheme := "ws"
	base := c.baseURL
	switch {
	case strings.HasPrefix(base, "https://"):
		scheme = "wss"
		base = strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = strings.TrimPrefix(base, "http://")
	}
	return fmt.Sprintf("%s://%s/api/tv/channels/%s/feed", scheme, base, channelID)
}

func (c *backendClient) postJSON(ctx context.Context, path string, payload any) (*http.Response, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("tvcapture: marshal %s body: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("tvcapture: build %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tvcapture: post %s: %w", path, err)
	}
	return resp, nil
}

// statusError wraps a non-2xx response into an error carrying the operation and
// status. The body is not included, keeping any presigned-URL echo out of logs.
func statusError(op string, resp *http.Response) error {
	return fmt.Errorf("tvcapture: %s: unexpected status %d", op, resp.StatusCode)
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func decodeJSON(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}
