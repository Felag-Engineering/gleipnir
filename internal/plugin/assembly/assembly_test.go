//go:build substratev2

package assembly

import (
	"context"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

func withDetectConfig(cfg container.DetectConfig) Option { return WithDetectConfig(cfg) }

func withRuntime(rt container.Runtime) Option { return WithRuntime(rt) }

func withSelfContainerID(id container.ContainerID) Option {
	return func(o *options) { o.selfContainerID = &id }
}

// The real check reads this host's /proc/sys, which a test must not depend on.
func withNoForwarding() Option {
	return func(o *options) { o.checkForwarding = func() error { return nil } }
}

// An hour keeps the periodic loop from running a pass the test did not ask for.
func withSlowReconciler() Option {
	return func(o *options) { o.reconcileInterval = time.Hour }
}

func newDeps(t *testing.T, pub *testutil.RecordingPublisher) Deps {
	t.Helper()
	store := testutil.NewTestStore(t)
	return Deps{
		Config: config.Config{
			PluginSubnetPool:         "10.83.0.0/16",
			OAuthRefreshInterval:     time.Hour,
			OAuthRefreshLead:         time.Hour,
			PluginDedupSweepInterval: time.Hour,
			PluginsDir:               t.TempDir(),
		},
		Store:       store,
		Publisher:   pub,
		Arbiter:     toolregistry.New(),
		MCPRegistry: mcp.NewRegistry(store.Queries()),
	}
}

func guardedListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	guarded, err := netguard.Wrap(ln, netip.MustParsePrefix("10.83.0.0/16"), nil)
	if err != nil {
		t.Fatalf("netguard.Wrap: %v", err)
	}
	return guarded
}

func rawListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func TestNew_Posture(t *testing.T) {
	dockerSocket := container.DetectConfig{
		Mode: "docker", DockerSocketPath: "/var/run/docker.sock", DockerSocketExists: true,
	}

	tests := []struct {
		name        string
		detect      container.DetectConfig
		wantErr     string
		wantEnabled bool
	}{
		{
			name:    "explicit docker without a socket fails",
			detect:  container.DetectConfig{Mode: "docker", DockerSocketPath: "/var/run/docker.sock"},
			wantErr: "docker",
		},
		{
			name:    "explicit rootless podman without a socket fails",
			detect:  container.DetectConfig{Mode: "rootless-podman", PodmanSocketPath: "/run/user/1000/podman/podman.sock"},
			wantErr: "rootless-podman",
		},
		{
			name:    "unknown mode fails",
			detect:  container.DetectConfig{Mode: "bogus"},
			wantErr: "invalid GLEIPNIR_CONTAINER_RUNTIME_MODE",
		},
		{
			name:        "auto without a socket disables plugins",
			detect:      container.DetectConfig{Mode: "auto"},
			wantEnabled: false,
		},
		{
			name:        "unset mode without a socket disables plugins",
			detect:      container.DetectConfig{},
			wantEnabled: false,
		},
		{
			name:        "explicit docker with a socket enables plugins",
			detect:      dockerSocket,
			wantEnabled: true,
		},
		{
			name:        "manual enables the assembly without a socket",
			detect:      container.DetectConfig{Mode: "manual"},
			wantEnabled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := New(context.Background(), newDeps(t, &testutil.RecordingPublisher{}),
				withDetectConfig(tt.detect), withRuntime(container.NewFake()))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("New error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(a.Shutdown)

			if a.enabled != tt.wantEnabled {
				t.Errorf("enabled = %v, want %v", a.enabled, tt.wantEnabled)
			}
			if gotHealth := a.AdminDeps().CapabilityHealth != nil; gotHealth != tt.wantEnabled {
				t.Errorf("CapabilityHealth present = %v, want %v", gotHealth, tt.wantEnabled)
			}
			if tt.wantEnabled {
				assertWired(t, a)
			}
			if a.AdminDeps().ManifestSnap == nil {
				t.Error("ManifestSnap must be non-nil whether or not plugins are enabled")
			}
		})
	}
}

func TestOperatorListenerReady(t *testing.T) {
	passEvents := []string{
		reconciler.EventPassCompleted,
		reconciler.EventRotationPassCompleted,
		reconciler.EventObservePassCompleted,
	}

	tests := []struct {
		name         string
		detect       container.DetectConfig
		listener     func(*testing.T) net.Listener
		wantErr      bool
		wantStarted  bool
		wantPassSeen bool
	}{
		{
			name:     "unguarded listener is refused and nothing starts",
			detect:   container.DetectConfig{Mode: "docker", DockerSocketExists: true, DockerSocketPath: "/var/run/docker.sock"},
			listener: rawListener,
			wantErr:  true,
		},
		{
			name:     "unguarded listener is refused even when plugins are disabled",
			detect:   container.DetectConfig{Mode: "auto"},
			listener: rawListener,
			wantErr:  true,
		},
		{
			name:         "guarded listener runs the boot pass",
			detect:       container.DetectConfig{Mode: "docker", DockerSocketExists: true, DockerSocketPath: "/var/run/docker.sock"},
			listener:     guardedListener,
			wantStarted:  true,
			wantPassSeen: true,
		},
		{
			name:     "guarded listener with plugins disabled starts nothing",
			detect:   container.DetectConfig{Mode: "auto"},
			listener: guardedListener,
		},
		{
			name:        "manual posture starts the assembly but runs no pass",
			detect:      container.DetectConfig{Mode: "manual"},
			listener:    guardedListener,
			wantStarted: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &testutil.RecordingPublisher{}
			a, err := New(context.Background(), newDeps(t, pub),
				withDetectConfig(tt.detect), withRuntime(container.NewFake()), withNoForwarding(), withSlowReconciler())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(a.Shutdown)

			err = a.OperatorListenerReady(tt.listener(t))
			if (err != nil) != tt.wantErr {
				t.Fatalf("OperatorListenerReady error = %v, wantErr %v", err, tt.wantErr)
			}
			if started := a.reconciler != nil; started != tt.wantStarted {
				t.Errorf("reconciler started = %v, want %v", started, tt.wantStarted)
			}

			// The boot pass is synchronous, so its events have been published by
			// the time OperatorListenerReady returned; Shutdown first makes the
			// read race-free against the loop it started.
			a.Shutdown()
			var seen []string
			for _, e := range pub.Events {
				for _, want := range passEvents {
					if e.Type == want {
						seen = append(seen, e.Type)
					}
				}
			}
			if !tt.wantPassSeen {
				if len(seen) != 0 {
					t.Fatalf("passes ran but none were expected: %v", seen)
				}
				return
			}
			// Only the published passes are order-checked here (core, rotation,
			// observe); attach publishes nothing and is covered by
			// TestOperatorListenerReady_RunsAttachPass.
			if len(seen) < len(passEvents) || !reflect.DeepEqual(seen[:len(passEvents)], passEvents) {
				t.Errorf("boot pass events = %v, want %v first", seen, passEvents)
			}
		})
	}
}

// assertWired catches a mis-assignment in compose: each field must hold the
// component it is named for, not merely something that satisfies its interface.
func assertWired(t *testing.T, a *Assembly) {
	t.Helper()
	if _, ok := a.prober.(*caphealth.Prober); !ok {
		t.Errorf("prober = %T, want *caphealth.Prober", a.prober)
	}
	if _, ok := a.collector.(*resources.Collector); !ok {
		t.Errorf("collector = %T, want *resources.Collector", a.collector)
	}
	host, hostOK := a.hostListeners.(*hostendpoint.ListenerSet)
	proxy, proxyOK := a.egressProxy.(*hostendpoint.ListenerSet)
	if !hostOK || !proxyOK {
		t.Fatalf("listener sets = %T, %T, want *hostendpoint.ListenerSet", a.hostListeners, a.egressProxy)
	}
	if host == proxy {
		t.Error("host endpoint and egress proxy share one ListenerSet")
	}
	if _, ok := a.runtime.(container.Runtime); !ok {
		t.Errorf("runtime = %T, want a container.Runtime", a.runtime)
	}
	if a.newReconciler == nil {
		t.Fatal("newReconciler is nil")
	}
	guard := guardedListener(t).(*netguard.Listener).Guard()
	rec, err := a.newReconciler(guard, nil)
	if err != nil {
		t.Fatalf("newReconciler: %v", err)
	}
	if rec == nil {
		t.Error("reconciler factory built a nil *reconciler.Reconciler")
	}
}

// A managed endpoint whose instance has no live generation is withdrawn by the
// attach pass, so its disappearance proves the pass ran in the boot sequence.
func TestOperatorListenerReady_RunsAttachPass(t *testing.T) {
	deps := newDeps(t, &testutil.RecordingPublisher{})
	a, err := New(context.Background(), deps,
		withDetectConfig(container.DetectConfig{Mode: "docker", DockerSocketExists: true, DockerSocketPath: "/var/run/docker.sock"}),
		withRuntime(container.NewFake()), withNoForwarding(), withSlowReconciler())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(a.Shutdown)

	ctx := context.Background()
	q := deps.Store.Queries()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: "plug", Name: "plug", PluginVersion: "1.0.0", ManifestSnapshot: "{}",
		TrustedPubkey: "k", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: "inst", PluginID: "plug", InstanceName: "inst", ConfigJson: "{}",
		SubscriptionScopeJson: "{}", HandshakeVersions: "{}", HealthState: "healthy",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, nil)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	if _, err := registrar.Register(ctx, mcp.ManagedEndpoint{InstanceID: "inst", InstanceName: "inst", URL: "http://10.83.0.2:9000"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := a.OperatorListenerReady(guardedListener(t)); err != nil {
		t.Fatalf("OperatorListenerReady: %v", err)
	}

	rows, err := q.ListManagedMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListManagedMCPServers: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("managed rows after boot = %d, want 0: the attach pass did not run", len(rows))
	}
}

func TestOperatorListenerReady_SecondCallFails(t *testing.T) {
	a, err := New(context.Background(), newDeps(t, &testutil.RecordingPublisher{}),
		withDetectConfig(container.DetectConfig{Mode: "manual"}), withRuntime(container.NewFake()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(a.Shutdown)

	if err := a.OperatorListenerReady(guardedListener(t)); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := a.OperatorListenerReady(guardedListener(t)); err == nil {
		t.Fatal("second call succeeded; the boot sequence must run once")
	}
}

// stepLog records the order in which fakes are driven.
type stepLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *stepLog) record(step string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.steps = append(l.steps, step)
}

func (l *stepLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.steps...)
}

type fakeLoop struct {
	name string
	log  *stepLog
}

func (f fakeLoop) Start(context.Context) error { f.log.record(f.name + ".start"); return nil }
func (f fakeLoop) Stop()                       { f.log.record(f.name + ".stop") }
func (f fakeLoop) Wait()                       { f.log.record(f.name + ".wait") }

type fakeCloser struct {
	name string
	log  *stepLog
}

func (f fakeCloser) Close(context.Context) error { f.log.record(f.name + ".close"); return nil }

type fakeRuntimeCloser struct{ log *stepLog }

func (f fakeRuntimeCloser) Close() error { f.log.record("runtime.close"); return nil }

type fakeEvents struct{ log *stepLog }

func (f fakeEvents) Start(_ context.Context, id string)   { f.log.record("events.start:" + id) }
func (f fakeEvents) Restart(_ context.Context, id string) { f.log.record("events.restart:" + id) }
func (f fakeEvents) Stop(id string)                       { f.log.record("events.stop:" + id) }
func (f fakeEvents) StopAll()                             { f.log.record("events.stop_all") }

func TestShutdown_Order(t *testing.T) {
	log := &stepLog{}
	a := &Assembly{
		prober:        fakeLoop{"prober", log},
		collector:     fakeLoop{"collector", log},
		hostListeners: fakeCloser{"host_listeners", log},
		egressProxy:   fakeCloser{"egress_listeners", log},
		reconciler:    fakeLoop{"reconciler", log},
		runtime:       fakeRuntimeCloser{log},
	}

	a.Shutdown()

	want := []string{
		"prober.stop", "prober.wait",
		"collector.stop", "collector.wait",
		"reconciler.stop", "reconciler.wait",
		"host_listeners.close", "egress_listeners.close",
		"runtime.close",
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("shutdown order:\n got  %v\n want %v", got, want)
	}

	// Shutdown is reachable from more than one exit path.
	a.Shutdown()
	if got := log.snapshot(); len(got) != len(want) {
		t.Errorf("second Shutdown drove components again: %v", got[len(want):])
	}
}

func TestShutdown_PartiallyBuiltAssembly(t *testing.T) {
	t.Run("plugins disabled", func(t *testing.T) {
		(&Assembly{}).Shutdown()
	})

	t.Run("operator listener never became ready", func(t *testing.T) {
		log := &stepLog{}
		a := &Assembly{
			prober:        fakeLoop{"prober", log},
			collector:     fakeLoop{"collector", log},
			hostListeners: fakeCloser{"host_listeners", log},
			egressProxy:   fakeCloser{"egress_listeners", log},
		}

		a.Shutdown()

		want := []string{
			"prober.stop", "prober.wait",
			"collector.stop", "collector.wait",
			"host_listeners.close", "egress_listeners.close",
		}
		if got := log.snapshot(); !reflect.DeepEqual(got, want) {
			t.Errorf("shutdown steps:\n got  %v\n want %v", got, want)
		}
	})
}

func TestQuiesce(t *testing.T) {
	log := &stepLog{}
	(&Assembly{events: fakeEvents{log}}).Quiesce()
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"events.stop_all"}) {
		t.Errorf("Quiesce steps = %v, want the supervisor joined", got)
	}

	// No supervisor yet (and plugins disabled) must be safe.
	(&Assembly{}).Quiesce()
}
