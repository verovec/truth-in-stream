package crawlnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// slackHTTPTimeout bounds a single webhook POST; an unreachable or slow Slack
// must not pin the run announcing the event.
const slackHTTPTimeout = 10 * time.Second

// SlackNotifier posts crawl events to a Slack incoming webhook as a Block Kit
// message. It mirrors the digest poster's transport (stdlib http.Client with a
// timeout, a JSON Block Kit body) rather than adding a Slack dependency, and
// keeps the webhook URL - a secret - out of every returned error so it can never
// be logged.
type SlackNotifier struct {
	httpClient *http.Client
	webhookURL string
}

// NewSlackNotifier builds a notifier that posts to webhookURL.
func NewSlackNotifier(webhookURL string) *SlackNotifier {
	return &SlackNotifier{
		httpClient: &http.Client{Timeout: slackHTTPTimeout},
		webhookURL: webhookURL,
	}
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlock struct {
	Type string     `json:"type"`
	Text *slackText `json:"text,omitempty"`
}

type slackMessage struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

// Notify posts the event's one-line summary to the configured webhook. The
// summary is the same plain line every transport renders, carried both as the
// notification fallback text and as a mrkdwn section block.
func (n *SlackNotifier) Notify(ctx context.Context, event CrawlEvent) error {
	summary := event.message()
	body, err := json.Marshal(slackMessage{
		Text: summary,
		Blocks: []slackBlock{
			{Type: "section", Text: &slackText{Type: "mrkdwn", Text: summary}},
		},
	})
	if err != nil {
		return fmt.Errorf("crawlnotify: marshal slack message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		// A URL parse failure returns a *url.Error whose message embeds the
		// webhook URL; keep the secret out of the returned (and logged) error.
		return errors.New("crawlnotify: invalid slack webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		// err may embed the webhook URL; do not surface it into a logged message.
		return errors.New("crawlnotify: post to slack failed")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("crawlnotify: slack returned status %d", resp.StatusCode)
	}
	return nil
}
