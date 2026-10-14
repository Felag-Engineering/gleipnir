//go:build substratev2

package assembly

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader/loadertest"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

var dockerSocket = container.DetectConfig{Mode: "docker", DockerSocketPath: "/var/run/docker.sock", DockerSocketExists: true}

// installFixture is an assembly over container.Fake, composed but not started.
type installFixture struct {
	a    *Assembly
	deps Deps
	q    *db.Queries
	rt   *container.Fake
}

// newInstallFixture composes the assembly in the given posture. mutate adjusts
// the dependencies before New sees them.
func newInstallFixture(t *testing.T, detect container.DetectConfig, mutate func(*Deps), opts ...Option) *installFixture {
	t.Helper()
	deps := newDeps(t, &testutil.RecordingPublisher{})
	if mutate != nil {
		mutate(&deps)
	}

	rt := container.NewFake()
	selfID, err := rt.Create(context.Background(), container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
	})
	if err != nil {
		t.Fatalf("creating gleipnir's own container: %v", err)
	}
	all := append([]Option{
		withDetectConfig(detect), withRuntime(rt), withSelfContainerID(selfID), withNoForwarding(), withSlowReconciler(),
	}, opts...)
	a, err := New(context.Background(), deps, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(a.Shutdown)
	return &installFixture{a: a, deps: deps, q: deps.Store.Queries(), rt: rt}
}

func (f *installFixture) install(t *testing.T, path string) (string, error) {
	t.Helper()
	return f.a.AdminDeps().Installer.Install(context.Background(), path)
}

func (f *installFixture) plugin(t *testing.T, name string) db.Plugin {
	t.Helper()
	p, err := f.q.GetPluginByName(context.Background(), name)
	if err != nil {
		t.Fatalf("GetPluginByName(%q): %v", name, err)
	}
	return p
}

func (f *installFixture) hasPlugin(t *testing.T, name string) bool {
	t.Helper()
	_, err := f.q.GetPluginByName(context.Background(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("GetPluginByName(%q): %v", name, err)
	}
	return true
}

func (f *installFixture) auditCount(t *testing.T, eventType string) int {
	t.Helper()
	rows, err := f.q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{EventType: eventType, Limit: 100})
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByType(%q): %v", eventType, err)
	}
	return len(rows)
}

func TestInstall_SignedBundleLandsPendingReviewWithImageLoaded(t *testing.T) {
	f := newInstallFixture(t, dockerSocket, nil)
	b := loadertest.Build(t, loadertest.Options{Name: "acme-plugin", Version: "1.0.0", Seed: "signed"})
	f.rt.PendingImages = []container.ImageInfo{{ID: b.Digest, SizeBytes: 2048}}

	id, err := f.install(t, b.Path)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	p := f.plugin(t, "acme-plugin")
	if p.ID != id {
		t.Errorf("Install returned %q, row is %q", id, p.ID)
	}
	if p.Status != string(model.PluginStatusPendingReview) {
		t.Errorf("status = %q, want pending_review: signed first installs wait for an admin", p.Status)
	}
	if p.BinaryPath != nil {
		t.Errorf("binary_path = %q, want NULL for a containerized plugin", *p.BinaryPath)
	}
	if f.rt.Loads != 1 {
		t.Errorf("runtime image loads = %d, want 1", f.rt.Loads)
	}
	img, err := f.q.GetContainerImage(context.Background(), b.Digest)
	if err != nil {
		t.Fatalf("GetContainerImage: %v", err)
	}
	if img.PluginID == nil || *img.PluginID != id {
		t.Errorf("image plugin_id = %v, want %q", img.PluginID, id)
	}
}

func TestInstall_SignatureAndUnsignedPolicy(t *testing.T) {
	tests := []struct {
		name          string
		allowUnsigned bool
		unsigned      bool
		wantInstalled bool
	}{
		{name: "unsigned is refused by default", unsigned: true, wantInstalled: false},
		{name: "unsigned installs under the operator opt-in", allowUnsigned: true, unsigned: true, wantInstalled: true},
		{name: "signed installs with the opt-in on too", allowUnsigned: true, wantInstalled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstallFixture(t, dockerSocket, func(d *Deps) { d.Config.AllowUnsignedPlugins = tt.allowUnsigned })
			b := loadertest.Build(t, loadertest.Options{Name: "acme-plugin", Version: "1.0.0", Seed: "s", Unsigned: tt.unsigned})
			f.rt.PendingImages = []container.ImageInfo{{ID: b.Digest}}

			_, err := f.install(t, b.Path)
			if tt.wantInstalled && err != nil {
				t.Fatalf("Install: %v", err)
			}
			if got := f.hasPlugin(t, "acme-plugin"); got != tt.wantInstalled {
				t.Errorf("plugin row present = %v, want %v (err=%v)", got, tt.wantInstalled, err)
			}
			wantLoads := 0
			if tt.wantInstalled {
				wantLoads = 1
			}
			if f.rt.Loads != wantLoads {
				t.Errorf("image loads = %d, want %d: a refused bundle must not reach the runtime", f.rt.Loads, wantLoads)
			}
		})
	}
}

// The pin is first-use: a bundle for the same plugin signed with a different
// key is held for an admin, and nothing about the installed plugin changes.
func TestInstall_DifferentKeyIsHeldNotInstalled(t *testing.T) {
	f := newInstallFixture(t, dockerSocket, nil)
	first := loadertest.Build(t, loadertest.Options{Name: "acme-plugin", Version: "1.0.0", Seed: "v1"})
	f.rt.PendingImages = []container.ImageInfo{{ID: first.Digest}}
	if _, err := f.install(t, first.Path); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	pinned := f.plugin(t, "acme-plugin")

	second := loadertest.Build(t, loadertest.Options{Name: "acme-plugin", Version: "2.0.0", Seed: "v2"})
	_, err := f.install(t, second.Path)
	var rejected *loader.InstallRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("second Install error = %v, want *loader.InstallRejectedError", err)
	}

	after := f.plugin(t, "acme-plugin")
	if after.PluginVersion != "1.0.0" || after.TrustedPubkey != pinned.TrustedPubkey {
		t.Errorf("plugin changed to version %q key %q; a different key must not replace the pinned one", after.PluginVersion, after.TrustedPubkey)
	}
	if f.rt.Loads != 1 {
		t.Errorf("image loads = %d, want 1: the held bundle's image must not be loaded", f.rt.Loads)
	}
}

// Manual posture hands the assembly a read-only runtime, so the image load is
// refused by the runtime itself. The install still commits.
func TestInstall_ManualPostureRecordsWithoutLoadingImage(t *testing.T) {
	f := newInstallFixture(t, container.DetectConfig{Mode: "manual"}, nil)
	b := loadertest.Build(t, loadertest.Options{Name: "acme-plugin", Version: "1.0.0", Seed: "manual"})

	if _, err := f.install(t, b.Path); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if f.rt.Loads != 0 {
		t.Errorf("image loads = %d, want 0 in manual posture", f.rt.Loads)
	}
	if got := f.plugin(t, "acme-plugin").Status; got != string(model.PluginStatusPendingReview) {
		t.Errorf("status = %q, want pending_review", got)
	}
	if got := f.auditCount(t, "plugin_image_load_skipped"); got != 1 {
		t.Errorf("plugin_image_load_skipped events = %d, want 1", got)
	}
}

func TestAdminDeps_InstallAndCredentialSurface(t *testing.T) {
	tests := []struct {
		name        string
		key         []byte
		wantCredsOn bool
	}{
		{name: "with an encryption key", key: make([]byte, 32), wantCredsOn: true},
		{name: "without an encryption key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newInstallFixture(t, dockerSocket, func(d *Deps) { d.EncryptionKey = tt.key })
			d := f.a.AdminDeps()

			if d.Installer == nil {
				t.Error("Installer is nil; the upload route would answer 503")
			}
			if d.PluginsDir != f.deps.Config.PluginsDir {
				t.Errorf("PluginsDir = %q, want %q", d.PluginsDir, f.deps.Config.PluginsDir)
			}
			if got := d.OAuthHandler != nil; got != tt.wantCredsOn {
				t.Errorf("OAuthHandler set = %v, want %v", got, tt.wantCredsOn)
			}
			if got := d.CredentialsHandler != nil; got != tt.wantCredsOn {
				t.Errorf("CredentialsHandler set = %v, want %v", got, tt.wantCredsOn)
			}
			if got := d.CredentialSeeder != nil; got != tt.wantCredsOn {
				t.Errorf("CredentialSeeder set = %v, want %v", got, tt.wantCredsOn)
			}
			if got := d.OnPublicURLChanged != nil; got != tt.wantCredsOn {
				t.Errorf("OnPublicURLChanged set = %v, want %v", got, tt.wantCredsOn)
			}
			if d.OptionsHandler != nil {
				t.Error("OptionsHandler is set; it proxies a gRPC service containerized plugins do not serve")
			}
		})
	}
}

func TestAdminDeps_DisabledAssemblyHasNoInstaller(t *testing.T) {
	deps := newDeps(t, &testutil.RecordingPublisher{})
	a, err := New(context.Background(), deps, withDetectConfig(container.DetectConfig{Mode: "auto"}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(a.Shutdown)

	if a.AdminDeps().Installer != nil {
		t.Error("a plugins-disabled assembly must not offer an installer")
	}
}

// installSignals reports each install the directory watcher completes.
type installSignals struct {
	done chan installed
}

type installed struct {
	path string
	id   string
	err  error
}

func (s *installSignals) wrap(inner loader.BundleInstaller) loader.BundleInstaller {
	return loader.BundleInstallerFunc(func(ctx context.Context, tarPath string) (string, error) {
		id, err := inner.Install(ctx, tarPath)
		s.done <- installed{path: tarPath, id: id, err: err}
		return id, err
	})
}

func (s *installSignals) await(t *testing.T) installed {
	t.Helper()
	select {
	case got := <-s.done:
		return got
	case <-time.After(30 * time.Second):
		t.Fatal("the watcher never installed the bundle")
		return installed{}
	}
}

func newWatchFixture(t *testing.T) (*installFixture, *installSignals) {
	t.Helper()
	signals := &installSignals{done: make(chan installed, 8)}
	f := newInstallFixture(t, dockerSocket, nil, func(o *options) {
		o.watchedInstaller = signals.wrap
		o.watcherDebounce = time.Millisecond
	})
	return f, signals
}

// A bundle dropped into the plugins directory is installed once the operator
// listener gate has opened: by the same installer, with the same outcome as an
// upload.
func TestWatcher_InstallsDroppedBundleAfterGate(t *testing.T) {
	f, signals := newWatchFixture(t)
	b := loadertest.Build(t, loadertest.Options{Name: "dropped", Version: "1.0.0", Seed: "dropped"})
	f.rt.PendingImages = []container.ImageInfo{{ID: b.Digest}}

	if err := f.a.OperatorListenerReady(guardedListener(t)); err != nil {
		t.Fatalf("OperatorListenerReady: %v", err)
	}
	dropped := filepath.Join(f.deps.Config.PluginsDir, "dropped-1.0.0.tar.gz")
	if err := os.Rename(b.Path, dropped); err != nil {
		t.Fatalf("dropping the bundle: %v", err)
	}

	got := signals.await(t)
	if got.err != nil {
		t.Fatalf("watcher install: %v", got.err)
	}
	if got.path != dropped {
		t.Errorf("installed %q, want %q", got.path, dropped)
	}
	if p := f.plugin(t, "dropped"); p.ID != got.id || p.Status != string(model.PluginStatusPendingReview) {
		t.Errorf("plugin = %+v, want the installed pending_review row %q", p, got.id)
	}
	if f.rt.Loads != 1 {
		t.Errorf("image loads = %d, want 1", f.rt.Loads)
	}
}

// A bundle already in the directory at boot is picked up by the initial sweep,
// not only a drop that happens while running.
func TestWatcher_SweepsBundleAlreadyPresentAtBoot(t *testing.T) {
	f, signals := newWatchFixture(t)
	b := loadertest.Build(t, loadertest.Options{Name: "waiting", Version: "1.0.0", Seed: "waiting"})
	f.rt.PendingImages = []container.ImageInfo{{ID: b.Digest}}
	if err := os.Rename(b.Path, filepath.Join(f.deps.Config.PluginsDir, "waiting-1.0.0.tar.gz")); err != nil {
		t.Fatalf("placing the bundle: %v", err)
	}

	if err := f.a.OperatorListenerReady(guardedListener(t)); err != nil {
		t.Fatalf("OperatorListenerReady: %v", err)
	}

	if got := signals.await(t); got.err != nil {
		t.Fatalf("sweep install: %v", got.err)
	}
	if !f.hasPlugin(t, "waiting") {
		t.Error("the bundle present at boot was not installed")
	}
}

// Without the netguard proof nothing starts, the watcher included: an install
// must not run against a substrate that was refused its gate.
func TestWatcher_NotStartedWithoutGate(t *testing.T) {
	f, signals := newWatchFixture(t)
	b := loadertest.Build(t, loadertest.Options{Name: "early", Version: "1.0.0", Seed: "early"})
	if err := os.Rename(b.Path, filepath.Join(f.deps.Config.PluginsDir, "early-1.0.0.tar.gz")); err != nil {
		t.Fatalf("placing the bundle: %v", err)
	}

	err := f.a.OperatorListenerReady(rawListener(t))
	if err == nil || !strings.Contains(err.Error(), "netguard") {
		t.Fatalf("OperatorListenerReady(raw) = %v, want a netguard refusal", err)
	}
	f.a.Shutdown() // joins every goroutine the assembly started; an installed bundle would have signalled.

	select {
	case got := <-signals.done:
		t.Fatalf("the watcher installed %q without the gate", got.path)
	default:
	}
}

// Shutdown cancels the watcher and joins it with the other background loops.
func TestWatcher_ShutdownJoinsWatcher(t *testing.T) {
	f, _ := newWatchFixture(t)
	if err := f.a.OperatorListenerReady(guardedListener(t)); err != nil {
		t.Fatalf("OperatorListenerReady: %v", err)
	}

	joined := make(chan struct{})
	go func() {
		f.a.Shutdown()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(30 * time.Second):
		t.Fatal("Shutdown did not return: the watcher was not stopped")
	}
}
