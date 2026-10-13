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

	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

func withDetectConfig(cfg container.DetectConfig) Option {
	return func(o *options) { o.detect = &cfg }
}

func withRuntime(rt container.Runtime) Option {
	return func(o *options) { o.runtime = rt }
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
	return Deps{
		Config:    config.Config{PluginSubnetPool: "10.83.0.0/16"},
		Store:     testutil.NewTestStore(t),
		Publisher: pub,
		Arbiter:   toolregistry.New(),
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
			// Core, then rotation, then observe: attach runs between rotation
			// and observe, and observe is last (reconcile -> attach -> observe).
			if len(seen) < len(passEvents) || !reflect.DeepEqual(seen[:len(passEvents)], passEvents) {
				t.Errorf("boot pass events = %v, want %v first", seen, passEvents)
			}
		})
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

func (f fakeEvents) StopAll() { f.log.record("events.stop_all") }

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

	// The listener sets are closed a second time after the reconciler is
	// joined: a pass racing the first Close could have re-added a listener.
	want := []string{
		"prober.stop", "prober.wait",
		"collector.stop", "collector.wait",
		"host_listeners.close", "egress_listeners.close",
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
