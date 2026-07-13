package tvcapture

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeClient struct {
	mu       sync.Mutex
	channels []Channel
	listErr  error
	prunes   []int
}

func (f *fakeClient) setChannels(chs []Channel, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels = chs
	f.listErr = err
}

func (f *fakeClient) ListChannels(context.Context) ([]Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.channels, f.listErr
}

func (f *fakeClient) Prune(_ context.Context, days int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes = append(f.prunes, days)
	return len(f.prunes), nil
}

type fakeSupervisor struct {
	ch      Channel
	started atomic.Bool
	stopped atomic.Bool
}

func (f *fakeSupervisor) start(context.Context) { f.started.Store(true) }
func (f *fakeSupervisor) stop()                 { f.stopped.Store(true) }

func TestDiffChannels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		running     map[string]Channel
		enabled     []Channel
		wantStartID []string
		wantStop    []string
	}{
		{
			name:        "start newly enabled",
			running:     map[string]Channel{},
			enabled:     []Channel{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}},
			wantStartID: []string{"a", "b"},
		},
		{
			name:     "stop disabled channel still running",
			running:  map[string]Channel{"a": {ID: "a", Enabled: true}},
			enabled:  []Channel{{ID: "a", Enabled: false}},
			wantStop: []string{"a"},
		},
		{
			name:     "stop channel that disappeared",
			running:  map[string]Channel{"a": {ID: "a", Enabled: true}},
			enabled:  []Channel{},
			wantStop: []string{"a"},
		},
		{
			name:    "no change when already running and enabled with same config",
			running: map[string]Channel{"a": {ID: "a", Enabled: true, SourceRef: "u", SourceKind: "hls", ArchiveEnabled: true}},
			enabled: []Channel{{ID: "a", Enabled: true, SourceRef: "u", SourceKind: "hls", ArchiveEnabled: true}},
		},
		{
			name:        "restart when source ref changes",
			running:     map[string]Channel{"a": {ID: "a", Enabled: true, SourceRef: "old"}},
			enabled:     []Channel{{ID: "a", Enabled: true, SourceRef: "new"}},
			wantStartID: []string{"a"},
			wantStop:    []string{"a"},
		},
		{
			name:        "restart when archive toggled",
			running:     map[string]Channel{"a": {ID: "a", Enabled: true, ArchiveEnabled: false}},
			enabled:     []Channel{{ID: "a", Enabled: true, ArchiveEnabled: true}},
			wantStartID: []string{"a"},
			wantStop:    []string{"a"},
		},
		{
			name:    "name-only change does not restart",
			running: map[string]Channel{"a": {ID: "a", Enabled: true, Name: "Old"}},
			enabled: []Channel{{ID: "a", Enabled: true, Name: "New"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start, stop := diffChannels(tc.running, tc.enabled)
			var startIDs []string
			for _, c := range start {
				startIDs = append(startIDs, c.ID)
			}
			sort.Strings(startIDs)
			sort.Strings(stop)
			if !equalStrings(startIDs, tc.wantStartID) {
				t.Errorf("toStart = %v, want %v", startIDs, tc.wantStartID)
			}
			if !equalStrings(stop, tc.wantStop) {
				t.Errorf("toStop = %v, want %v", stop, tc.wantStop)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newManagerHarness(client channelLister) (*Manager, map[string]*fakeSupervisor, *sync.Mutex) {
	created := map[string]*fakeSupervisor{}
	var mu sync.Mutex
	factory := func(ch Channel) channelSupervisor {
		mu.Lock()
		defer mu.Unlock()
		fs := &fakeSupervisor{ch: ch}
		created[ch.ID] = fs
		return fs
	}
	m := NewManager(client, factory, 5*time.Millisecond, 30, discardLogger())
	return m, created, &mu
}

// reconcileSync runs one reconcile and waits for any background supervisor stops
// it launched to finish, so the test can assert stop state synchronously.
func reconcileSync(ctx context.Context, m *Manager, running map[string]channelSupervisor, snapshots map[string]Channel) {
	var wg sync.WaitGroup
	m.reconcile(ctx, running, snapshots, &wg)
	wg.Wait()
}

func TestManagerReconcileStartStop(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	m, created, mu := newManagerHarness(client)
	running := map[string]channelSupervisor{}
	snapshots := map[string]Channel{}
	ctx := context.Background()

	// Enable c1 -> started.
	client.setChannels([]Channel{{ID: "c1", Slug: "tf1", Enabled: true}}, nil)
	reconcileSync(ctx, m, running, snapshots)
	mu.Lock()
	c1 := created["c1"]
	mu.Unlock()
	if c1 == nil || !c1.started.Load() {
		t.Fatalf("c1 not started: %+v", c1)
	}

	// Disable c1 -> stopped and removed.
	client.setChannels([]Channel{{ID: "c1", Slug: "tf1", Enabled: false}}, nil)
	reconcileSync(ctx, m, running, snapshots)
	if !c1.stopped.Load() {
		t.Fatal("c1 not stopped after disable")
	}
	if _, ok := running["c1"]; ok {
		t.Fatal("c1 still in running set after disable")
	}
}

func TestManagerReconcileChannelDisappears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	m, created, mu := newManagerHarness(client)
	running := map[string]channelSupervisor{}
	snapshots := map[string]Channel{}
	ctx := context.Background()

	client.setChannels([]Channel{{ID: "c1", Enabled: true}}, nil)
	reconcileSync(ctx, m, running, snapshots)
	client.setChannels([]Channel{}, nil)
	reconcileSync(ctx, m, running, snapshots)

	mu.Lock()
	c1 := created["c1"]
	mu.Unlock()
	if !c1.stopped.Load() {
		t.Fatal("disappeared channel not stopped")
	}
}

func TestManagerReconcileKeepsSetOnListError(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	m, created, mu := newManagerHarness(client)
	running := map[string]channelSupervisor{}
	snapshots := map[string]Channel{}
	ctx := context.Background()

	client.setChannels([]Channel{{ID: "c1", Enabled: true}}, nil)
	reconcileSync(ctx, m, running, snapshots)

	client.setChannels(nil, errors.New("transient"))
	reconcileSync(ctx, m, running, snapshots)

	mu.Lock()
	c1 := created["c1"]
	mu.Unlock()
	if c1.stopped.Load() {
		t.Fatal("transient list error must not stop running supervisor")
	}
	if _, ok := running["c1"]; !ok {
		t.Fatal("c1 dropped from running set on transient error")
	}
}

func TestManagerReconcileRestartsOnConfigChange(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	m, created, mu := newManagerHarness(client)
	running := map[string]channelSupervisor{}
	snapshots := map[string]Channel{}
	ctx := context.Background()

	client.setChannels([]Channel{{ID: "c1", Slug: "tf1", Enabled: true, SourceRef: "old"}}, nil)
	reconcileSync(ctx, m, running, snapshots)
	mu.Lock()
	old := created["c1"]
	mu.Unlock()
	if old == nil || !old.started.Load() {
		t.Fatal("c1 not started initially")
	}

	// Change the source ref -> the stale supervisor stops and a fresh one starts.
	client.setChannels([]Channel{{ID: "c1", Slug: "tf1", Enabled: true, SourceRef: "new"}}, nil)
	reconcileSync(ctx, m, running, snapshots)
	mu.Lock()
	cur := created["c1"]
	mu.Unlock()
	if !old.stopped.Load() {
		t.Fatal("stale supervisor not stopped on config change")
	}
	if cur == old {
		t.Fatal("expected a fresh supervisor after config change")
	}
	if !cur.started.Load() {
		t.Fatal("new supervisor not started")
	}
	if cur.ch.SourceRef != "new" {
		t.Fatalf("new supervisor started with source ref %q, want new", cur.ch.SourceRef)
	}

	// A reconcile with identical config must not restart the supervisor.
	reconcileSync(ctx, m, running, snapshots)
	mu.Lock()
	after := created["c1"]
	mu.Unlock()
	if after != cur || cur.stopped.Load() {
		t.Fatal("unchanged channel must not restart the supervisor")
	}
}

func TestManagerRunPrunesOnStartup(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	m, _, _ := newManagerHarness(client)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	// The retention ticker is 24h; a prune seen here can only be the startup one.
	waitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.prunes) >= 1
	}, "prune invoked on startup")

	cancel()
	<-done
}

func TestManagerPruneRecordsCall(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	m, _, _ := newManagerHarness(client)
	m.prune(context.Background())
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.prunes) != 1 || client.prunes[0] != 30 {
		t.Fatalf("prunes = %v, want [30]", client.prunes)
	}
}

func TestManagerRunStopsAllOnShutdown(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	client.setChannels([]Channel{{ID: "c1", Slug: "tf1", Enabled: true}}, nil)
	m, created, mu := newManagerHarness(client)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return created["c1"] != nil && created["c1"].started.Load()
	}, "c1 supervisor started")

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !created["c1"].stopped.Load() {
		t.Fatal("supervisor not stopped on manager shutdown")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
