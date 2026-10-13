//go:build substratev2

package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// signalWithin fails the test if ch does not fire; the deadline is a CI bound
// for an in-process hand-off, not a duration anything depends on.
func signalWithin(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func newCancelableAssembly() *Assembly {
	ctx, cancel := context.WithCancel(context.Background())
	return &Assembly{ctx: ctx, cancel: cancel}
}

// Shutdown must stop and join the loops on bg even when nothing upstream
// cancelled the context New was given: the assembly owns that cancel.
func TestShutdown_JoinsBackgroundLoops(t *testing.T) {
	a := newCancelableAssembly()

	started := make(chan struct{})
	var exited atomic.Bool
	a.goBackground(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		exited.Store(true)
	})
	signalWithin(t, started, "the background loop to start")

	a.Shutdown()

	if !exited.Load() {
		t.Error("Shutdown returned before the background loop exited")
	}
}

type fakePoller struct{ log *stepLog }

func (f fakePoller) Start(context.Context, time.Duration) { f.log.record("poller.start") }
func (f fakePoller) Wait()                                { f.log.record("poller.wait") }

func TestShutdown_OrderWithPollerAndEvents(t *testing.T) {
	log := &stepLog{}
	a := &Assembly{
		events:        fakeEvents{log},
		prober:        fakeLoop{"prober", log},
		collector:     fakeLoop{"collector", log},
		reconciler:    fakeLoop{"reconciler", log},
		hostListeners: fakeCloser{"host_listeners", log},
		egressProxy:   fakeCloser{"egress_listeners", log},
		poller:        fakePoller{log},
		cancel:        func() { log.record("ctx.cancel") },
		runtime:       fakeRuntimeCloser{log},
	}

	a.Shutdown()

	want := []string{
		"events.stop_all",
		"prober.stop", "prober.wait",
		"collector.stop", "collector.wait",
		"reconciler.stop", "reconciler.wait",
		"host_listeners.close", "egress_listeners.close",
		"ctx.cancel", "poller.wait",
		"runtime.close",
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("shutdown order:\n got  %v\n want %v", got, want)
	}
}

type fakeGC struct {
	passes chan struct{}
	err    error
}

func (f fakeGC) ReconcileGC(context.Context) (reconciler.GCResult, error) {
	f.passes <- struct{}{}
	return reconciler.GCResult{}, f.err
}

// The cleanup pass is scheduled on the bg group, keeps running after a failed
// pass (the next one finds the same leftovers), and stops with Shutdown.
func TestScheduleGC(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "successful passes repeat"},
		{name: "a failed pass does not stop the schedule", err: errors.New("daemon unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newCancelableAssembly()
			gc := fakeGC{passes: make(chan struct{}, 16), err: tt.err}

			a.scheduleGC(gc, time.Millisecond)
			for i := 0; i < 2; i++ {
				select {
				case <-gc.passes:
				case <-time.After(10 * time.Second):
					t.Fatalf("pass %d never ran", i+1)
				}
			}

			a.Shutdown()
		})
	}
}

func TestNewBackgroundLoops(t *testing.T) {
	base := newDeps(t, &testutil.RecordingPublisher{})

	tests := []struct {
		name string
		key  []byte
		want []string
	}{
		{
			name: "no encryption key: only the dedup sweeper",
			want: []string{"dedup sweeper"},
		},
		{
			name: "encryption key: credential refresh and nonce pruning join it",
			key:  make([]byte, 32),
			want: []string{"dedup sweeper", "oauth nonce janitor", "oauth refresh scanner"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loops := newBackgroundLoops(Deps{Config: base.Config, EncryptionKey: tt.key}, base.Store.Queries())
			var got []string
			for _, l := range loops {
				got = append(got, l.name)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("loops = %v, want %v", got, tt.want)
			}
		})
	}
}

type startErrEvents struct {
	fakeEvents
	err error
}

func (s startErrEvents) StartAll(context.Context) error { return s.err }

// The consumers of the converged world start only after the boot pass: the
// task poller, every composed background loop, and the events streams.
func TestOperatorListenerReady_StartsConsumers(t *testing.T) {
	newReady := func(t *testing.T) *Assembly {
		a, err := New(context.Background(), newDeps(t, &testutil.RecordingPublisher{}),
			withDetectConfig(container.DetectConfig{Mode: "manual"}), withRuntime(container.NewFake()), withSlowReconciler())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(a.Shutdown)
		return a
	}

	t.Run("background loops and events streams start", func(t *testing.T) {
		a := newReady(t)
		log := &stepLog{}
		looped := make(chan struct{})
		a.events = fakeEvents{log}
		a.backgrounds = []backgroundLoop{{name: "probe", run: func(ctx context.Context) {
			close(looped)
			<-ctx.Done()
		}}}

		if err := a.OperatorListenerReady(guardedListener(t)); err != nil {
			t.Fatalf("OperatorListenerReady: %v", err)
		}

		signalWithin(t, looped, "the composed background loop to start")
		if got := log.snapshot(); !reflect.DeepEqual(got, []string{"events.start_all"}) {
			t.Errorf("events steps = %v, want one StartAll", got)
		}
	})

	t.Run("a failing events start is fatal to boot", func(t *testing.T) {
		a := newReady(t)
		a.events = startErrEvents{fakeEvents: fakeEvents{&stepLog{}}, err: errors.New("cannot list plugins")}

		if err := a.OperatorListenerReady(guardedListener(t)); err == nil {
			t.Fatal("OperatorListenerReady succeeded although the events streams could not start")
		}
	})

	t.Run("no bound launcher means no streams and no error", func(t *testing.T) {
		a := newReady(t)

		if err := a.OperatorListenerReady(guardedListener(t)); err != nil {
			t.Fatalf("OperatorListenerReady: %v", err)
		}
	})
}

func TestLauncherDeps(t *testing.T) {
	tests := []struct {
		name    string
		detect  container.DetectConfig
		enabled bool
	}{
		{name: "plugins enabled", detect: container.DetectConfig{Mode: "manual"}, enabled: true},
		{name: "plugins disabled", detect: container.DetectConfig{Mode: "auto"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(context.Background(), newDeps(t, &testutil.RecordingPublisher{}),
				withDetectConfig(tt.detect), withRuntime(container.NewFake()))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(a.Shutdown)

			d := a.LauncherDeps()
			// A nil interface is what "plugins disabled" means to the launcher;
			// a typed nil would read as enabled and then panic.
			if got := d.ApprovalDispatcher != nil; got != tt.enabled {
				t.Errorf("ApprovalDispatcher set = %v, want %v", got, tt.enabled)
			}
			if got := d.FeedbackDispatcher != nil; got != tt.enabled {
				t.Errorf("FeedbackDispatcher set = %v, want %v", got, tt.enabled)
			}
			if got := d.ToolClassifier != nil; got != tt.enabled {
				t.Errorf("ToolClassifier set = %v, want %v", got, tt.enabled)
			}
			// Managed tools are MCP tools: nothing routes through a plugin pool.
			if d.Dispatcher != nil || d.Registrar != nil || d.ToolResolver != nil || d.Canceller != nil {
				t.Errorf("v1 dispatch fields must stay nil: %+v", d)
			}
			if tt.enabled && a.notifier == nil {
				t.Error("notifier was not composed")
			}
		})
	}
}

func TestNew_RequiresSharedRegistry(t *testing.T) {
	deps := newDeps(t, &testutil.RecordingPublisher{})
	deps.MCPRegistry = nil

	_, err := New(context.Background(), deps,
		withDetectConfig(container.DetectConfig{Mode: "manual"}), withRuntime(container.NewFake()))
	if err == nil {
		t.Fatal("New succeeded without a registry; the assembly would have built a private one")
	}
}

// gatedLauncher is a plugintrigger.RunLauncher that behaves like the real one
// where it matters here: it registers a run with the RunManager (the
// WaitGroup add) from inside the call.
type gatedLauncher struct {
	mgr *runpkg.RunManager
	log *stepLog

	entered       chan struct{} // closed when the first Launch is in flight
	cancelSeen    chan struct{} // closed when that Launch observes its stream cancelled
	release       chan struct{} // the test closes it to let the Launch finish
	enteredOnce   sync.Once
	cancelOnce    sync.Once
	registerOnce  sync.Once
	launchRunning atomic.Int32
}

func newGatedLauncher(mgr *runpkg.RunManager, log *stepLog) *gatedLauncher {
	return &gatedLauncher{
		mgr: mgr, log: log,
		entered:    make(chan struct{}),
		cancelSeen: make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (l *gatedLauncher) LaunchWithConcurrency(ctx context.Context, _ runpkg.LaunchParams) (runpkg.LaunchResult, error) {
	l.launchRunning.Add(1)
	defer l.launchRunning.Add(-1)
	l.enteredOnce.Do(func() { close(l.entered) })

	// Quiesce cancels the stream before it joins it; seeing that is how the
	// test knows the shutdown has reached the point where it must wait for us.
	<-ctx.Done()
	l.cancelOnce.Do(func() { close(l.cancelSeen) })
	<-l.release

	l.registerOnce.Do(func() {
		runCtx, cancel := context.WithCancel(context.Background())
		l.mgr.Register("run-1", cancel, make(chan bool, 1))
		l.log.record("launch.registered")
		go func() {
			<-runCtx.Done()
			l.mgr.Deregister("run-1")
		}()
	})
	return runpkg.LaunchResult{Outcome: runpkg.OutcomeLaunched}, nil
}

func eventsManifest(t *testing.T) string {
	t.Helper()
	b, err := sdkmanifest.Marshal(&sdkmanifest.Manifest{
		SchemaVersion: "1", Name: "events-plugin", Version: "1.0.0",
		EventKinds: []sdkmanifest.EventKindDecl{{Kind: "message"}},
	})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b)
}

func subscribedPolicyYAML(t *testing.T, source string) string {
	t.Helper()
	b, err := yaml.Marshal(map[string]any{
		"name":    "on-message",
		"trigger": map[string]any{"type": "subscribed", "source": source, "event_kind": "message"},
		"agent": map[string]any{
			"task":  "do stuff",
			"model": map[string]any{"provider": "anthropic", "name": "claude-3-5-haiku-latest"},
		},
	})
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	return string(b)
}

// The events supervisor must be stopped AND joined before the run drain.
//
// A Launch is held in flight, then shutdown runs in each order. Joined first,
// the in-flight Launch finishes and registers its run before the drain begins,
// so the drain waits for it. Drained first, the drain returns with nothing
// registered and the run registers afterwards — the add-after-Wait that races
// teardown. The swapped case is the control: it shows this harness can see the
// violation, so the passing case means something.
func TestQuiesce_JoinsEventStreamsBeforeRunDrain(t *testing.T) {
	tests := []struct {
		name         string
		quiesceFirst bool
		wantOrder    []string
	}{
		{
			name:         "quiesce then drain",
			quiesceFirst: true,
			wantOrder:    []string{"launch.registered", "quiesce.returned", "drain.returned"},
		},
		{
			name:         "drain then quiesce (the violation)",
			quiesceFirst: false,
			wantOrder:    []string{"drain.returned", "launch.registered", "quiesce.returned"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := mcp.NewFakeEventsServer()
			fake.HeartbeatMs = 50
			fake.ListenEvents = []mcp.CloudEvent{
				{SpecVersion: "1.0", Source: "src", Type: "message", ID: "evt-1", Sequence: 1, Data: json.RawMessage(`{}`)},
			}
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)

			deps := newDeps(t, &testutil.RecordingPublisher{})
			q := deps.Store.Queries()
			seedInstance(t, q, "inst-1", eventsManifest(t))
			testutil.InsertPolicy(t, deps.Store, "pol-1", "on-message", "subscribed", subscribedPolicyYAML(t, "inst-1"))
			registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, nil)
			if err != nil {
				t.Fatalf("NewManagedRegistrar: %v", err)
			}
			if _, err := registrar.Register(context.Background(), mcp.ManagedEndpoint{
				InstanceID: "inst-1", InstanceName: "inst-1", URL: srv.URL,
			}); err != nil {
				t.Fatalf("Register: %v", err)
			}

			// Registered after the store, so it runs before the store closes;
			// the supervisor's cleanup below is registered after it, so the
			// trigger source is joined before the manager drain.
			mgr := runpkg.NewRunManager()
			t.Cleanup(mgr.Wait)

			a, err := New(context.Background(), deps,
				withDetectConfig(container.DetectConfig{Mode: "manual"}), withRuntime(container.NewFake()), withSlowReconciler())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			log := &stepLog{}
			launcher := newGatedLauncher(mgr, log)
			a.bind(launcher)
			// Routing consults liveness; in production the prober records it.
			a.health.SetLiveness("inst-1", caphealth.Liveness{ContainerHealthy: true, DiscoverOK: true})
			t.Cleanup(func() {
				select {
				case <-launcher.release:
				default:
					close(launcher.release)
				}
				a.Shutdown()
			})

			if err := a.events.StartAll(a.ctx); err != nil {
				t.Fatalf("StartAll: %v", err)
			}
			signalWithin(t, launcher.entered, "the event to reach the launcher")

			quiesceDone := make(chan struct{})
			quiesce := func() {
				go func() {
					a.Quiesce()
					log.record("quiesce.returned")
					close(quiesceDone)
				}()
			}
			drain := func() {
				mgr.CancelAll()
				mgr.Wait()
				log.record("drain.returned")
			}

			if tt.quiesceFirst {
				quiesce()
				signalWithin(t, launcher.cancelSeen, "Quiesce to cancel the stream")
				close(launcher.release)
				signalWithin(t, quiesceDone, "Quiesce to return")
				drain()
			} else {
				drain()
				quiesce()
				signalWithin(t, launcher.cancelSeen, "Quiesce to cancel the stream")
				close(launcher.release)
				signalWithin(t, quiesceDone, "Quiesce to return")
				mgr.CancelAll()
				mgr.Wait()
			}

			if got := log.snapshot(); !reflect.DeepEqual(got, tt.wantOrder) {
				t.Errorf("order:\n got  %v\n want %v", got, tt.wantOrder)
			}
			if n := launcher.launchRunning.Load(); n != 0 {
				t.Errorf("%d Launch calls still in flight after Quiesce returned", n)
			}
		})
	}
}
