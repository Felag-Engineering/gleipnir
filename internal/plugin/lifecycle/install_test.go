package lifecycle_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	pluginpkg "github.com/felag-engineering/gleipnir/internal/plugin"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/lifecycle"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// digestOf renders a plausible sha256 digest from a seed, mirroring
// internal/plugin/loader's own test helper of the same name (unexported
// there, so this package needs its own).
func digestOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeUnsignedV2Bundle writes an unsigned v2 bundle tarball (manifest.yaml +
// image.tar, no signing.pub/.minisig) into dir. Unsigned keeps the test
// focused on the BundleInstaller wiring rather than on Minisign, by installing
// under AllowUnsigned permissive mode.
func writeUnsignedV2Bundle(t *testing.T, dir, name, digest string) {
	t.Helper()

	manifest := fmt.Sprintf(`schema_version: "2"
name: %s
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/%s@%s
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`, name, name, digest)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range []struct {
		name    string
		content []byte
	}{
		{"manifest.yaml", []byte(manifest)},
		{"image.tar", []byte("fake OCI image archive for " + name)},
	} {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", e.name, err)
		}
		if _, err := tw.Write(e.content); err != nil {
			t.Fatalf("write tar body %q: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".tar.gz"), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
}

// TestOCIInstallerAdapter_WatcherInstallsV2Bundle proves the BundleInstaller
// seam end to end: a *loader.Watcher, given an ociInstallerAdapter wrapping a
// *loader.OCIInstaller over a container.Fake runtime, installs a dropped v2
// bundle exactly as a v1 installer would a v1 one (#952 DoD).
func TestOCIInstallerAdapter_WatcherInstallsV2Bundle(t *testing.T) {
	store := testutil.NewTestStore(t)
	dropDir := t.TempDir()

	base := loader.NewInstaller(&pluginpkg.Verifier{AllowUnsigned: true}, store.Queries(), store.DB(), nil, t.TempDir())
	rt := container.NewFake()
	digest := digestOf("lifecycle-adapter-test")
	rt.PendingImages = []container.ImageInfo{{ID: digest, SizeBytes: 2048}}

	adapter := lifecycle.NewOCIInstallerAdapter(loader.NewOCIInstaller(base, rt))
	w := loader.NewWatcher(dropDir, adapter, loader.WithDebounce(20*time.Millisecond))

	fw, err := w.Setup()
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, fw) }()

	writeUnsignedV2Bundle(t, dropDir, "acme-tool", digest)

	// Poll for the installed row, mirroring internal/plugin/loader's own
	// watcher tests (stubInstaller.waitForCount) — there is no publisher wired
	// here to synchronize on instead. The 5s deadline is generously above the
	// 20ms debounce window this test otherwise waits out.
	deadline := time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		if plugin, err := store.Queries().GetPluginByName(ctx, "acme-tool"); err == nil {
			if plugin.BinaryPath != nil {
				t.Errorf("binary_path = %v, want NULL for a v2 (containerized) install", *plugin.BinaryPath)
			}
			found = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("watcher did not install the v2 bundle within the deadline")
	}

	cancel()
	<-done
}
