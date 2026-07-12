package tvcapture

import (
	"context"
	"log/slog"
	"time"
)

// retentionInterval is how often the manager prunes recordings past retention.
const retentionInterval = 24 * time.Hour

// channelLister is the subset of the backend client the manager reconciles from
// and prunes through.
type channelLister interface {
	ListChannels(ctx context.Context) ([]Channel, error)
	Prune(ctx context.Context, retentionDays int) (int, error)
}

// supervisorFactory builds a per-channel supervisor. It is a seam so the manager
// can be tested with fake supervisors.
type supervisorFactory func(ch Channel) channelSupervisor

// Manager reconciles the set of running per-channel supervisors against the
// backend's enabled channels on a poll ticker, and prunes old recordings daily.
type Manager struct {
	client        channelLister
	newSupervisor supervisorFactory
	poll          time.Duration
	retentionDays int
	logger        *slog.Logger
}

// NewManager builds a reconcile manager.
func NewManager(client channelLister, factory supervisorFactory, poll time.Duration, retentionDays int, logger *slog.Logger) *Manager {
	return &Manager{
		client:        client,
		newSupervisor: factory,
		poll:          poll,
		retentionDays: retentionDays,
		logger:        logger,
	}
}

// Run reconciles immediately, then on every poll tick, until ctx is canceled;
// on shutdown it stops every running supervisor. A failed poll keeps the current
// set rather than tearing everything down on a transient error.
func (m *Manager) Run(ctx context.Context) {
	running := make(map[string]channelSupervisor)
	defer func() {
		for _, sup := range running {
			sup.stop()
		}
	}()

	pollTicker := time.NewTicker(m.poll)
	defer pollTicker.Stop()
	retentionTicker := time.NewTicker(retentionInterval)
	defer retentionTicker.Stop()

	m.reconcile(ctx, running)

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			m.reconcile(ctx, running)
		case <-retentionTicker.C:
			m.prune(ctx)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context, running map[string]channelSupervisor) {
	channels, err := m.client.ListChannels(ctx)
	if err != nil {
		m.logger.Warn("tvcapture: list channels failed, keeping current set", slog.Any("err", err))
		return
	}

	runningIDs := make(map[string]bool, len(running))
	for id := range running {
		runningIDs[id] = true
	}
	toStart, toStop := diffChannels(runningIDs, channels)

	for _, id := range toStop {
		m.logger.Info("tvcapture: stopping supervisor", slog.String("channel_id", id))
		running[id].stop()
		delete(running, id)
	}
	for _, ch := range toStart {
		m.logger.Info("tvcapture: starting supervisor",
			slog.String("channel_id", ch.ID), slog.String("slug", ch.Slug))
		sup := m.newSupervisor(ch)
		sup.start(ctx)
		running[ch.ID] = sup
	}
}

func (m *Manager) prune(ctx context.Context) {
	deleted, err := m.client.Prune(ctx, m.retentionDays)
	if err != nil {
		m.logger.Warn("tvcapture: prune recordings failed", slog.Any("err", err))
		return
	}
	m.logger.Info("tvcapture: pruned recordings", slog.Int("deleted", deleted))
}

// diffChannels compares the currently-running channel ids against the desired
// enabled channels and returns which to start and which to stop. A channel is
// desired only when it is enabled; anything running but not desired (disabled or
// gone) is stopped. It is pure so the reconcile decision is unit-testable.
func diffChannels(running map[string]bool, enabled []Channel) (toStart []Channel, toStop []string) {
	desired := make(map[string]bool, len(enabled))
	for _, ch := range enabled {
		if !ch.Enabled {
			continue
		}
		desired[ch.ID] = true
		if !running[ch.ID] {
			toStart = append(toStart, ch)
		}
	}
	for id := range running {
		if !desired[id] {
			toStop = append(toStop, id)
		}
	}
	return toStart, toStop
}
