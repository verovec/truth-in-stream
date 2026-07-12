package tvcapture

import (
	"log/slog"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// NewWorker wires the capture worker from config and returns its reconcile
// Manager, ready to Run. It installs the production implementations for every
// side effect (process exec, backend HTTP, WebSocket feed, filesystem archive)
// and the real clock; tests build the Manager and supervisor from their fakes
// directly rather than through this.
func NewWorker(cfg config.TVCapture, httpClient *http.Client, notifier crawlnotify.Notifier, logger *slog.Logger) *Manager {
	tokens := newTokenSource(httpClient, cfg.TokenURL, cfg.ClientID, cfg.ClientSecret)
	client := newBackendClient(cfg.BackendBaseURL, httpClient, tokens)
	runner := newExecRunner(logger)
	feed := newWSFeedConnector(client, tokens)
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
