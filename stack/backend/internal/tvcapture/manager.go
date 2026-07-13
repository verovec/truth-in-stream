package tvcapture

import (
	"context"
	"log/slog"
	"sync"
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

// Run reconciles immediately, prunes once at startup, then reconciles on every
// poll tick and prunes on every retention tick, until ctx is canceled; on
// shutdown it stops every running supervisor. A failed poll keeps the current
// set rather than tearing everything down on a transient error. Pruning at
// startup (not only on the 24h tick) means a worker that restarts more often
// than the retention interval still enforces retention.
func (m *Manager) Run(ctx context.Context) {
	running := make(map[string]channelSupervisor)
	// snapshots records the Channel each running supervisor was started with, so
	// a later reconcile can detect a config change and restart it.
	snapshots := make(map[string]Channel)
	// stopping tracks supervisors whose stop() is in flight. Stops run in their
	// own goroutine so one channel's up-to-90s final-archive salvage never blocks
	// reconcile, the poll/retention ticks, or another channel starting; shutdown
	// waits on this so an in-flight salvage still completes.
	var stopping sync.WaitGroup
	defer func() {
		for id, sup := range running {
			m.stopAsync(&stopping, id, sup)
		}
		stopping.Wait()
	}()

	pollTicker := time.NewTicker(m.poll)
	defer pollTicker.Stop()
	retentionTicker := time.NewTicker(retentionInterval)
	defer retentionTicker.Stop()

	m.reconcile(ctx, running, snapshots, &stopping)
	m.prune(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			m.reconcile(ctx, running, snapshots, &stopping)
		case <-retentionTicker.C:
			m.prune(ctx)
		}
	}
}

// stopAsync stops a supervisor in its own goroutine, tracked by wg, and removes
// it from the caller's bookkeeping immediately. reconcile has already deleted the
// id from running/snapshots before calling this.
func (m *Manager) stopAsync(wg *sync.WaitGroup, id string, sup channelSupervisor) {
	m.logger.Info("tvcapture: stopping supervisor", slog.String("channel_id", id))
	wg.Add(1)
	go func() {
		defer wg.Done()
		sup.stop()
	}()
}

func (m *Manager) reconcile(ctx context.Context, running map[string]channelSupervisor, snapshots map[string]Channel, stopping *sync.WaitGroup) {
	channels, err := m.client.ListChannels(ctx)
	if err != nil {
		m.logger.Warn("tvcapture: list channels failed, keeping current set", slog.Any("err", err))
		return
	}

	toStart, toStop := diffChannels(snapshots, channels)

	// A channel present in both is a config-change restart. Its stop must finish
	// before its start, because the new capture reuses the same segment dir and
	// single-publisher feed slot as the draining old one; a concurrent old-salvage
	// would race the new ffmpeg's segment writes. A pure disable (only in toStop)
	// has no successor, so it stops in the background and never blocks.
	restarting := make(map[string]bool, len(toStart))
	for _, ch := range toStart {
		if _, ok := running[ch.ID]; ok {
			restarting[ch.ID] = true
		}
	}

	for _, id := range toStop {
		sup := running[id]
		delete(running, id)
		delete(snapshots, id)
		if restarting[id] {
			m.logger.Info("tvcapture: restarting supervisor (config changed)", slog.String("channel_id", id))
			sup.stop()
			continue
		}
		m.stopAsync(stopping, id, sup)
	}
	for _, ch := range toStart {
		m.logger.Info("tvcapture: starting supervisor",
			slog.String("channel_id", ch.ID), slog.String("slug", ch.Slug))
		sup := m.newSupervisor(ch)
		sup.start(ctx)
		running[ch.ID] = sup
		snapshots[ch.ID] = ch
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

// diffChannels compares the currently-running channels (by the snapshot each was
// started with) against the desired enabled channels and returns which to start
// and which to stop. A channel is desired only when it is enabled; anything
// running but not desired (disabled or gone) is stopped. A still-enabled channel
// whose capture-relevant config changed is put in BOTH results (stop the old
// supervisor, start a new one with the new config). It is pure so the reconcile
// decision is unit-testable.
func diffChannels(running map[string]Channel, enabled []Channel) (toStart []Channel, toStop []string) {
	desired := make(map[string]bool, len(enabled))
	for _, ch := range enabled {
		if !ch.Enabled {
			continue
		}
		desired[ch.ID] = true
		cur, ok := running[ch.ID]
		if !ok {
			toStart = append(toStart, ch)
			continue
		}
		if captureConfigChanged(cur, ch) {
			toStop = append(toStop, ch.ID)
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

// captureConfigChanged reports whether a running channel's capture-relevant
// config differs from its desired form, so the supervisor must be restarted.
// Only fields that change what or how the worker captures matter: the source
// transport/reference and whether archiving is on. Name is display-only.
func captureConfigChanged(a, b Channel) bool {
	return a.SourceKind != b.SourceKind ||
		a.SourceRef != b.SourceRef ||
		a.ArchiveEnabled != b.ArchiveEnabled
}
