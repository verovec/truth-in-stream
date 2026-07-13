package tvcapture

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// apiHTTPTimeout bounds every control-plane call (channel list, token fetch,
// recording register/prune) and the feed WebSocket handshake, so a hung backend
// or Keycloak cannot pin the worker. The recording upload is deliberately NOT
// bounded by this: a full stream-copied hour can be multiple GB and gets an
// unbounded client, bounded instead by the per-segment archive context.
const apiHTTPTimeout = 30 * time.Second

// NewWorker wires the capture worker from config and returns its reconcile
// Manager, ready to Run. It installs the production implementations for every
// side effect (process exec, backend HTTP, WebSocket feed, filesystem archive)
// and the real clock; tests build the Manager and supervisor from their fakes
// directly rather than through this.
func NewWorker(cfg config.TVCapture, notifier crawlnotify.Notifier, logger *slog.Logger) *Manager {
	apiClient := &http.Client{Timeout: apiHTTPTimeout}
	// No client timeout on uploads: a multi-GB hour must not be guillotined mid
	// PUT; the per-segment archive context is the real bound.
	uploadClient := &http.Client{}
	tokens := newTokenSource(apiClient, cfg.TokenURL, cfg.ClientID, cfg.ClientSecret)
	client := newBackendClient(cfg.BackendBaseURL, apiClient, uploadClient, tokens)
	runner := newExecRunner(logger)
	feed := newWSFeedConnector(client, tokens, apiClient)
	arch := newArchiver(client, cfg.FFmpegPath, logger)

	supCfg := supervisorConfig{
		WorkDir:        cfg.WorkDir,
		Segment:        cfg.SegmentDuration,
		FeedStall:      cfg.FeedStall,
		StreamlinkPath: cfg.StreamlinkPath,
		FFmpegPath:     cfg.FFmpegPath,
	}
	factory := func(ch Channel) channelSupervisor {
		return newSupervisor(ch, runner, feed, arch, realClock{}, supCfg, logger, notifier)
	}
	return NewManager(client, factory, cfg.PollInterval, cfg.RetentionDays, logger)
}
